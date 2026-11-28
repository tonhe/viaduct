package main

import (
	"bufio"
	"fmt"
	"net"
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
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			// A UTF-8 byte-order mark, which some editors write at the
			// start of a file, is not part of the first target.
			line = strings.TrimPrefix(line, "\uFEFF")
			first = false
		}
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

// targetKey is what two spellings of one target have in common: an address
// literal in its canonical form, a hostname in lower case without the dot
// that ends a fully qualified name. A name and the address it resolves to
// have different keys; nothing is looked up here.
func targetKey(t string) string {
	if ip := net.ParseIP(t); ip != nil {
		return ip.String()
	}
	return strings.TrimSuffix(strings.ToLower(t), ".")
}

// mergeTargets combines positional and file targets in order, removing
// duplicates (first occurrence wins, spelled as it was given) and enforcing
// the session cap. Two targets are duplicates when targetKey says so.
func mergeTargets(positional, fromFile []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, t := range append(append([]string{}, positional...), fromFile...) {
		key := targetKey(t)
		if seen[key] {
			continue
		}
		seen[key] = true
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
