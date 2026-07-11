// Package asn handles ASN enrichment via Team Cymru DNS lookups.
package asn

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tonhe/viaduct/internal/probe"
)

// ipVersion returns 4 or 6 for a parsed net.IP.
func ipVersion(ip net.IP) int {
	if ip.To4() != nil {
		return 4
	}
	return 6
}

// Info holds ASN data for an IP address.
type Info struct {
	Number int    // AS number (0 = unknown)
	Org    string // organization name
}

// Result is sent when ASN info is resolved for an IP.
type Result struct {
	IP   net.IP
	Info Info
}

// Enricher performs async ASN lookups with caching and deduplication.
type Enricher struct {
	workers    int
	cache      sync.Map // IP string -> Info
	pending    sync.Map // IP string -> struct{}
	reqCh      chan net.IP
	lookupFunc func(context.Context, net.IP) Info // extracted for testability; defaults to lookupCymru
}

// New creates an enricher with the given number of worker goroutines.
func New(workers int) *Enricher {
	return &Enricher{
		workers:    workers,
		reqCh:      make(chan net.IP, 256),
		lookupFunc: lookupCymru,
	}
}

// Lookup checks the cache for ASN info. Returns (Info{}, false) on miss.
func (e *Enricher) Lookup(ip net.IP) (Info, bool) {
	val, ok := e.cache.Load(ip.String())
	if !ok {
		return Info{}, false
	}
	return val.(Info), true
}

// Submit queues an IP for ASN lookup. Returns true if newly submitted,
// false if already pending/cached, nil, or private.
func (e *Enricher) Submit(ip net.IP) bool {
	if ip == nil {
		return false
	}

	key := ip.String()

	// Cache private IPs immediately
	if probe.IsPrivate(ip) {
		e.cache.Store(key, Info{Org: "Private"})
		return false
	}

	// Already resolved
	if _, ok := e.cache.Load(key); ok {
		return false
	}

	// Already pending
	if _, loaded := e.pending.LoadOrStore(key, struct{}{}); loaded {
		return false
	}

	select {
	case e.reqCh <- ip:
		return true
	default:
		// Channel full, drop
		e.pending.Delete(key)
		return false
	}
}

// Run starts worker goroutines. Results are sent to the provided channel.
// Blocks until ctx is cancelled.
func (e *Enricher) Run(ctx context.Context, results chan<- Result) {
	var wg sync.WaitGroup
	for i := 0; i < e.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.worker(ctx, results)
		}()
	}
	wg.Wait()
}

// FormatASN formats an ASN number and org name for display.
func FormatASN(number int, org string) string {
	if number > 0 && org != "" {
		return fmt.Sprintf("AS%d (%s)", number, org)
	}
	if number > 0 {
		return fmt.Sprintf("AS%d", number)
	}
	if org != "" {
		return org
	}
	return ""
}

// FormatASNShort formats just the AS number without the org name.
func FormatASNShort(number int) string {
	if number > 0 {
		return fmt.Sprintf("AS%d", number)
	}
	return ""
}

func (e *Enricher) worker(ctx context.Context, results chan<- Result) {
	for {
		select {
		case <-ctx.Done():
			return
		case ip := <-e.reqCh:
			info := e.lookupFunc(ctx, ip)

			key := ip.String()
			e.cache.Store(key, info)
			e.pending.Delete(key)

			if info.Number != 0 || info.Org != "" {
				select {
				case results <- Result{IP: ip, Info: info}:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

func lookupCymru(ctx context.Context, ip net.IP) Info {
	var info Info

	// Build reversed IP for DNS query
	name := probe.ASNReverseName(ipVersion(ip), ip)
	if name == "" {
		return info
	}

	// Query 1: origin lookup for AS number
	originCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	txts, err := net.DefaultResolver.LookupTXT(originCtx, name)
	cancel()

	if err != nil || len(txts) == 0 {
		return info
	}

	asn, err := parseOriginResponse(txts[0])
	if err != nil {
		return info
	}
	info.Number = asn

	// Query 2: AS name lookup for org
	nameCtx, cancel2 := context.WithTimeout(ctx, 2*time.Second)
	txts, err = net.DefaultResolver.LookupTXT(nameCtx, fmt.Sprintf("AS%d.asn.cymru.com", asn))
	cancel2()

	if err == nil && len(txts) > 0 {
		info.Org = parseASNameResponse(txts[0])
	}

	return info
}

// parseOriginResponse extracts the first ASN from a Team Cymru origin TXT response.
// Format: "13335 | 1.1.1.0/24 | US | arin | 2014-03-28"
// or multiple ASNs: "13335 15169 | 1.1.1.0/24 | US | arin | 2014-03-28"
func parseOriginResponse(txt string) (int, error) {
	if txt == "" {
		return 0, fmt.Errorf("empty response")
	}

	// Split on pipe, first field has ASN(s)
	parts := strings.SplitN(txt, "|", 2)
	asnField := strings.TrimSpace(parts[0])
	if asnField == "" {
		return 0, fmt.Errorf("empty ASN field")
	}

	// Take first ASN if multiple space-separated
	asnStr := strings.Fields(asnField)[0]
	n, err := strconv.Atoi(asnStr)
	if err != nil {
		return 0, fmt.Errorf("invalid ASN %q: %w", asnStr, err)
	}
	return n, nil
}

// parseASNameResponse extracts the org name (last field) from a Team Cymru AS name TXT response.
// Format: "13335 | US | arin | 2014-03-28 | CLOUDFLARENET"
func parseASNameResponse(txt string) string {
	if txt == "" {
		return ""
	}
	parts := strings.Split(txt, "|")
	return strings.TrimSpace(parts[len(parts)-1])
}

