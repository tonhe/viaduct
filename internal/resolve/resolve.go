// Package resolve handles reverse DNS resolution for hop IPs.
package resolve

import (
	"context"
	"net"
	"sync"
	"time"
)

// Result is sent when a hostname is resolved.
type Result struct {
	IP       net.IP
	Hostname string
}

// Resolver performs async reverse DNS lookups with caching and deduplication.
type Resolver struct {
	workers    int
	cache      sync.Map // IP string -> hostname string
	pending    sync.Map // IP string -> struct{} (dedup)
	reqCh      chan net.IP
	lookupFunc func(context.Context, net.IP) (string, error) // extracted for testability; defaults to real DNS
}

// defaultLookup performs a real reverse DNS lookup using the system resolver.
func defaultLookup(ctx context.Context, ip net.IP) (string, error) {
	names, err := net.DefaultResolver.LookupAddr(ctx, ip.String())
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", nil
	}
	return names[0], nil
}

// New creates a resolver with the given number of worker goroutines.
func New(workers int) *Resolver {
	return &Resolver{
		workers:    workers,
		reqCh:      make(chan net.IP, 256),
		lookupFunc: defaultLookup,
	}
}

// Lookup checks the cache for a hostname. Returns ("", false) on miss.
func (r *Resolver) Lookup(ip net.IP) (string, bool) {
	val, ok := r.cache.Load(ip.String())
	if !ok {
		return "", false
	}
	return val.(string), true
}

// Submit queues an IP for resolution. Returns true if newly submitted, false if already pending/cached.
func (r *Resolver) Submit(ip net.IP) bool {
	key := ip.String()

	// Already resolved
	if _, ok := r.cache.Load(key); ok {
		return false
	}

	// Already pending
	if _, loaded := r.pending.LoadOrStore(key, struct{}{}); loaded {
		return false
	}

	select {
	case r.reqCh <- ip:
		return true
	default:
		// Channel full, drop
		r.pending.Delete(key)
		return false
	}
}

// Run starts worker goroutines. Results are sent to the provided channel.
// Blocks until ctx is cancelled.
func (r *Resolver) Run(ctx context.Context, results chan<- Result) {
	var wg sync.WaitGroup
	for i := 0; i < r.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.worker(ctx, results)
		}()
	}
	wg.Wait()
}

func (r *Resolver) worker(ctx context.Context, results chan<- Result) {
	for {
		select {
		case <-ctx.Done():
			return
		case ip := <-r.reqCh:
			key := ip.String()

			resolveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			name, err := r.lookupFunc(resolveCtx, ip)
			cancel()

			var hostname string
			if err == nil && name != "" {
				hostname = name
				// Remove trailing dot
				if len(hostname) > 0 && hostname[len(hostname)-1] == '.' {
					hostname = hostname[:len(hostname)-1]
				}
			}

			r.cache.Store(key, hostname)
			r.pending.Delete(key)

			if hostname != "" {
				select {
				case results <- Result{IP: ip, Hostname: hostname}:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}
