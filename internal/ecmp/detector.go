// Package ecmp implements ECMP multipath discovery via flow-ID variation.
package ecmp

import "github.com/tonhe/viaduct/internal/hop"

// AnalysisResult holds the TTLs where ECMP paths diverge and converge.
type AnalysisResult struct {
	DivergencePoints  []int // TTLs where paths split
	ConvergencePoints []int // TTLs where paths merge
}

// Analyze scans a hop table and identifies divergence (single -> multi IP)
// and convergence (multi -> single IP) transition points.
func Analyze(table *hop.Table) AnalysisResult {
	var result AnalysisResult
	hops := table.Snapshot()
	if len(hops) < 2 {
		return result
	}
	for i := 0; i < len(hops); i++ {
		h := hops[i]
		nodeCount := len(h.GetNodes())
		if nodeCount <= 1 {
			if i > 0 && len(hops[i-1].GetNodes()) > 1 {
				result.ConvergencePoints = append(result.ConvergencePoints, h.TTL)
			}
		} else {
			if i == 0 || len(hops[i-1].GetNodes()) <= 1 {
				result.DivergencePoints = append(result.DivergencePoints, h.TTL)
			}
		}
	}
	return result
}
