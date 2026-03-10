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
)

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
	workers int
	cache   sync.Map // IP string -> Info
	pending sync.Map // IP string -> struct{}
	reqCh   chan net.IP
}

// New creates an enricher with the given number of worker goroutines.
func New(workers int) *Enricher {
	return &Enricher{
		workers: workers,
		reqCh:   make(chan net.IP, 256),
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
	if isPrivateIP(ip) {
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

func (e *Enricher) worker(ctx context.Context, results chan<- Result) {
	for {
		select {
		case <-ctx.Done():
			return
		case ip := <-e.reqCh:
			info := lookupCymru(ctx, ip)

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
	reversed := reverseIP(ip)
	if reversed == "" {
		return info
	}

	// Query 1: origin lookup for AS number
	originCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	txts, err := net.DefaultResolver.LookupTXT(originCtx, reversed+".origin.asn.cymru.com")
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

// reverseIP returns the reversed octets of an IPv4 address for DNS queries.
func reverseIP(ip net.IP) string {
	ip4 := ip.To4()
	if ip4 == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", ip4[3], ip4[2], ip4[1], ip4[0])
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

// isPrivateIP returns true for RFC 1918, loopback, and link-local addresses.
func isPrivateIP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}

	// 10.0.0.0/8
	if ip4[0] == 10 {
		return true
	}
	// 172.16.0.0/12
	if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
		return true
	}
	// 192.168.0.0/16
	if ip4[0] == 192 && ip4[1] == 168 {
		return true
	}
	// 127.0.0.0/8
	if ip4[0] == 127 {
		return true
	}
	// 169.254.0.0/16
	if ip4[0] == 169 && ip4[1] == 254 {
		return true
	}

	return false
}
