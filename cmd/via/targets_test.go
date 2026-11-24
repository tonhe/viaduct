package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTargetsFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "targets.txt")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseTargetsFile(t *testing.T) {
	path := writeTargetsFile(t, "example.com\n# comment\n\n  198.51.100.7  \nexample.com # trailing comment\n")
	got, err := parseTargetsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com", "198.51.100.7", "example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseTargetsFile_Missing(t *testing.T) {
	if _, err := parseTargetsFile(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestMergeTargets_DedupAndOrder(t *testing.T) {
	got, err := mergeTargets([]string{"a.example", "b.example"}, []string{"b.example", "c.example"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.example", "b.example", "c.example"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMergeTargets_CapEnforced(t *testing.T) {
	var many []string
	for i := 0; i < maxTargets+1; i++ {
		many = append(many, fmt.Sprintf("h%d.example", i))
	}
	if _, err := mergeTargets(many, nil); err == nil {
		t.Fatalf("want error above %d targets", maxTargets)
	}
}

func TestMergeTargets_Empty(t *testing.T) {
	if _, err := mergeTargets(nil, nil); err == nil {
		t.Fatal("want error for zero targets")
	}
}
