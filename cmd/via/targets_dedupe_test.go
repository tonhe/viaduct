package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Two spellings of one target are one session: names that differ in case or
// in the dot that ends a fully qualified name, and address literals written
// differently (viaduct#108 item 8). The first spelling given is kept.
func TestMergeTargets_DedupesSpellingsOfOneTarget(t *testing.T) {
	cases := []struct {
		name       string
		positional []string
		fromFile   []string
		want       []string
	}{
		{"case", []string{"example.com", "Example.COM"}, nil, []string{"example.com"}},
		{"the first spelling is the one kept", []string{"Example.com"}, []string{"example.com"}, []string{"Example.com"}},
		{"trailing dot", []string{"host.example."}, []string{"host.example"}, []string{"host.example."}},
		{"IPv6 literal, case and zeros", []string{"2001:db8::1", "2001:DB8:0:0:0:0:0:1"}, nil, []string{"2001:db8::1"}},
		{"IPv4-mapped literal and its IPv4 address", []string{"192.0.2.1", "::ffff:192.0.2.1"}, nil, []string{"192.0.2.1"}},
		{"different names stay apart", []string{"a.example", "b.example"}, []string{"A.example"}, []string{"a.example", "b.example"}},
		{"a name and an address stay apart: nothing is resolved here", []string{"host.example", "192.0.2.1"}, nil, []string{"host.example", "192.0.2.1"}},
		{"only one dot is taken off", []string{"host.example..", "host.example."}, nil, []string{"host.example..", "host.example."}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mergeTargets(tc.positional, tc.fromFile)
			if err != nil {
				t.Fatalf("mergeTargets: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("mergeTargets(%v, %v) = %v, want %v", tc.positional, tc.fromFile, got, tc.want)
			}
		})
	}
}

// Duplicates are removed before the cap is applied: 16 names and 16 respellings
// of them are 16 targets.
func TestMergeTargets_CapCountsDistinctTargets(t *testing.T) {
	var lower, upper []string
	for i := 0; i < maxTargets; i++ {
		lower = append(lower, string(rune('a'+i))+".example")
		upper = append(upper, string(rune('A'+i))+".EXAMPLE")
	}
	got, err := mergeTargets(lower, upper)
	if err != nil {
		t.Fatalf("mergeTargets: %v", err)
	}
	if !reflect.DeepEqual(got, lower) {
		t.Errorf("got %v, want the %d first spellings", got, maxTargets)
	}
}

// A UTF-8 byte-order mark at the start of a targets file is not part of the
// first target. One anywhere else is left alone.
func TestParseTargetsFile_ByteOrderMark(t *testing.T) {
	const bom = "\uFEFF"
	cases := []struct {
		name, content string
		want          []string
	}{
		{"before the first target", bom + "a.example\nb.example\n", []string{"a.example", "b.example"}},
		{"before a comment line", bom + "# list\na.example\n", []string{"a.example"}},
		{"a file that is only the mark", bom, nil},
		{"no mark", "a.example\n", []string{"a.example"}},
		{"not at the start of the file: kept", "a.example\n" + bom + "b.example\n", []string{"a.example", bom + "b.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "targets.txt")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := parseTargetsFile(path)
			if err != nil {
				t.Fatalf("parseTargetsFile: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("targets = %q, want %q", got, tc.want)
			}
		})
	}
}
