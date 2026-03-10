package hop

// DetectRateLimited identifies hops that appear to be ICMP rate-limiting.
// A hop is rate-limited if it has loss > 1% but all downstream hops have lower loss.
// Takes a snapshot of hops and maxTTL, returns a map of TTL -> bool.
func DetectRateLimited(hops []*Hop, maxTTL int) map[int]bool {
	hopMap := make(map[int]*Hop, len(hops))
	for _, h := range hops {
		hopMap[h.TTL] = h
	}

	rateLimited := make(map[int]bool)
	minDownstream := 100.0
	for ttl := maxTTL; ttl >= 1; ttl-- {
		h, ok := hopMap[ttl]
		if !ok || h.GetIP() == nil {
			continue
		}
		loss := h.LossPercent()
		if ttl < maxTTL && loss > 1.0 && minDownstream < loss-5.0 {
			rateLimited[ttl] = true
		}
		if loss < minDownstream {
			minDownstream = loss
		}
	}
	return rateLimited
}
