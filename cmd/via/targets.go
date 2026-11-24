package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// parseTargetsFile reads one host per line. '#' starts a comment (whole-line
// or trailing); blank lines are skipped. Duplicates are preserved here and
// removed by mergeTargets.
func parseTargetsFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("targets file: %w", err)
	}
	defer f.Close()

	var targets []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		targets = append(targets, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("targets file: %w", err)
	}
	return targets, nil
}

// mergeTargets combines positional and file targets in order, removing
// duplicates (first occurrence wins) and enforcing the session cap.
func mergeTargets(positional, fromFile []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, t := range append(append([]string{}, positional...), fromFile...) {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no targets specified")
	}
	if len(out) > maxTargets {
		return nil, fmt.Errorf("%d targets specified, max is %d", len(out), maxTargets)
	}
	return out, nil
}
