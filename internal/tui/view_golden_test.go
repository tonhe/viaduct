package tui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tonhe/viaduct/internal/theme"
)

var updateGoldens = flag.Bool("update-goldens", false, "update golden files instead of comparing")

// goldenCompare compares got against the file at path.
//   - If -update is set, writes got to path (creating parents as needed) and returns.
//   - Otherwise, reads path, normalizes line endings on both sides, compares byte-equal.
//   - On mismatch, reports a helpful error including the first diverging line.
func goldenCompare(t *testing.T, path string, got string) {
	t.Helper()
	got = normalizeLE(got)
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote golden %s (%d bytes)", path, len(got))
		return
	}
	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s not found (run with -update to create): %v", path, err)
	}
	want := normalizeLE(string(wantBytes))
	if want != got {
		t.Errorf("golden mismatch for %s\n%s", path, firstDiff(want, got))
	}
}

// firstDiff returns a short summary of where two strings first differ,
// expressed as a line-oriented message with the line number and both versions
// of the diverging line.
func firstDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")

	maxLen := len(wantLines)
	if len(gotLines) > maxLen {
		maxLen = len(gotLines)
	}

	for i := 0; i < maxLen; i++ {
		var wLine, gLine string
		if i < len(wantLines) {
			wLine = wantLines[i]
		}
		if i < len(gotLines) {
			gLine = gotLines[i]
		}
		if wLine != gLine {
			return fmt.Sprintf("first difference at line %d:\nwant: %q\n got: %q", i+1, wLine, gLine)
		}
	}
	// Same line content but different count (trailing newline difference).
	return fmt.Sprintf("content matches line-by-line but line counts differ: want %d, got %d",
		len(wantLines), len(gotLines))
}

// setTestTheme sets the active theme to solarized-dark for the duration of the
// test, restoring the previous theme via t.Cleanup.
//
// Note: theme.Current is a package-level var (not a function), so we snapshot
// its value directly before setting.
func setTestTheme(t *testing.T) {
	t.Helper()
	prev := theme.Current
	th := theme.ByName("solarized-dark")
	if th == nil {
		t.Fatal("solarized-dark theme not found — theme registry broken")
	}
	theme.Set(*th)
	t.Cleanup(func() {
		theme.Set(prev)
	})
}

// TestGolden_Framework is the done-signal for T8: internal round-trip test of
// the golden-file harness.
func TestGolden_Framework(t *testing.T) {
	// Round-trip: write a known file to a temp location, then verify goldenCompare
	// reads it back and accepts matching content.
	tmp := filepath.Join(t.TempDir(), "test.golden")

	content := "hello\nworld\n"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Read mode (updateGoldens is false during normal runs) — should pass.
	goldenCompare(t, tmp, content)

	// Line-ending normalization: golden has LF, got has CRLF → should still match.
	crlf := "hello\r\nworld\r\n"
	goldenCompare(t, tmp, crlf)

	// Verify mismatch detection via firstDiff directly (avoids marking the
	// parent test as failed through a sub-test that intentionally fails).
	diff := firstDiff("hello\n", "world\n")
	if diff == "" {
		t.Error("firstDiff returned empty for divergent strings")
	}
	if !strings.Contains(diff, "hello") && !strings.Contains(diff, "world") {
		t.Errorf("firstDiff = %q, expected to reference the diverging content", diff)
	}

	// Verify firstDiff also works for same-content-but-different-line-count edge.
	sameDiff := firstDiff("a\nb\n", "a\nb")
	if sameDiff == "" {
		t.Error("firstDiff returned empty for line-count mismatch")
	}
}
