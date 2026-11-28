package theme

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// builtinThemes is the number of themes the README advertises. Adding or
// removing a theme changes this number and the README together.
const builtinThemes = 61

var hexColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// baseColors returns a theme's sixteen colors with their names.
func baseColors(t Theme) []struct {
	name  string
	color lipgloss.Color
} {
	return []struct {
		name  string
		color lipgloss.Color
	}{
		{"Base00", t.Base00}, {"Base01", t.Base01}, {"Base02", t.Base02}, {"Base03", t.Base03},
		{"Base04", t.Base04}, {"Base05", t.Base05}, {"Base06", t.Base06}, {"Base07", t.Base07},
		{"Base08", t.Base08}, {"Base09", t.Base09}, {"Base0A", t.Base0A}, {"Base0B", t.Base0B},
		{"Base0C", t.Base0C}, {"Base0D", t.Base0D}, {"Base0E", t.Base0E}, {"Base0F", t.Base0F},
	}
}

func TestBuiltinThemes_NamesAndSlugs(t *testing.T) {
	slug := regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	seenSlug := make(map[string]string)
	seenName := make(map[string]string)
	for _, th := range All() {
		if strings.TrimSpace(th.Name) == "" {
			t.Errorf("theme with slug %q has no name", th.Slug)
		}
		// Slugs are typed on the command line and written in TOML.
		if !slug.MatchString(th.Slug) {
			t.Errorf("theme %q: slug %q, want lower-case words joined by hyphens", th.Name, th.Slug)
		}
		if other, dup := seenSlug[th.Slug]; dup {
			t.Errorf("slug %q is used by %q and %q", th.Slug, other, th.Name)
		}
		seenSlug[th.Slug] = th.Name
		if other, dup := seenName[th.Name]; dup {
			t.Errorf("name %q is used by %q and %q", th.Name, other, th.Slug)
		}
		seenName[th.Name] = th.Slug
	}
}

func TestBuiltinThemes_SixteenValidColors(t *testing.T) {
	for _, th := range All() {
		for _, c := range baseColors(th) {
			if !hexColor.MatchString(string(c.color)) {
				t.Errorf("theme %q: %s = %q, want #rrggbb in lower case", th.Slug, c.name, c.color)
			}
		}
		// Text drawn in the default foreground on the background must show.
		if th.Base05 == th.Base00 {
			t.Errorf("theme %q: foreground Base05 and background Base00 are both %q", th.Slug, th.Base00)
		}
	}
}

// The README says how many themes there are, three times, and lists every
// slug. A theme added or removed has to change it.
func TestBuiltinThemes_MatchTheREADME(t *testing.T) {
	if got := Count(); got != builtinThemes {
		t.Errorf("Count() = %d, want %d: update builtinThemes and the README together", got, builtinThemes)
	}
	if got := len(All()); got != Count() {
		t.Errorf("All() has %d themes, Count() = %d", got, Count())
	}
	if got := len(themes); got != Count() {
		t.Errorf("%d themes can be looked up by slug, %d are listed: a slug is used twice", got, Count())
	}

	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("reading the README: %v", err)
	}
	text := string(readme)
	for _, want := range []string{"| **61** |", "**61 color themes**", "ships with 61 built-in color themes", "Show all 61 themes"} {
		if !strings.Contains(text, want) {
			t.Errorf("README does not contain %q", want)
		}
	}
	start := strings.Index(text, "Show all 61 themes")
	end := strings.Index(text[start:], "</details>")
	if start < 0 || end < 0 {
		t.Fatal("README: the theme table was not found")
	}
	table := text[start : start+end]
	listed := make(map[string]bool)
	for _, line := range strings.Split(table, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 4 || !strings.HasPrefix(strings.TrimSpace(cells[1]), "**") {
			continue
		}
		for _, s := range strings.Split(cells[2], ",") {
			s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "(default)"))
			if s == "" {
				continue
			}
			if listed[s] {
				t.Errorf("README lists %q twice", s)
			}
			listed[s] = true
		}
	}
	for _, th := range All() {
		if !listed[th.Slug] {
			t.Errorf("README does not list the theme %q", th.Slug)
		}
		delete(listed, th.Slug)
	}
	for s := range listed {
		t.Errorf("README lists %q, which is not a built-in theme", s)
	}
}

func TestLookups(t *testing.T) {
	all := All()
	for i, th := range all {
		got := ByName(th.Slug)
		if got == nil || *got != th {
			t.Errorf("ByName(%q) = %v, want the theme %q", th.Slug, got, th.Name)
		}
		if got := ByIndex(i); got == nil || *got != th {
			t.Errorf("ByIndex(%d) = %v, want the theme %q", i, got, th.Slug)
		}
		if got := IndexOf(th.Slug); got != i {
			t.Errorf("IndexOf(%q) = %d, want %d", th.Slug, got, i)
		}
	}

	// An unknown slug, and one that differs only in case, are not found.
	for _, slug := range []string{"", "no-such-theme", "Solarized-Dark", "solarized dark"} {
		if got := ByName(slug); got != nil {
			t.Errorf("ByName(%q) = %q, want nil", slug, got.Slug)
		}
		if got := IndexOf(slug); got != -1 {
			t.Errorf("IndexOf(%q) = %d, want -1", slug, got)
		}
	}
	for _, i := range []int{-1, len(all), len(all) + 1} {
		if got := ByIndex(i); got != nil {
			t.Errorf("ByIndex(%d) = %q, want nil", i, got.Slug)
		}
	}

	names := Names()
	if len(names) != len(all) {
		t.Fatalf("Names() has %d entries, want %d", len(names), len(all))
	}
	for i, n := range names {
		if n[0] != all[i].Slug || n[1] != all[i].Name {
			t.Errorf("Names()[%d] = %v, want %q, %q", i, n, all[i].Slug, all[i].Name)
		}
	}
}

// All returns a copy: changing it does not change the built-in list.
func TestAll_ReturnsACopy(t *testing.T) {
	all := All()
	first := all[0]
	all[0] = Theme{Slug: "changed"}
	if got := All()[0]; got != first {
		t.Errorf("after changing the slice All returned, All()[0] = %q, want %q", got.Slug, first.Slug)
	}
	got := ByName(first.Slug)
	got.Name = "changed"
	if again := ByName(first.Slug); again.Name != first.Name {
		t.Errorf("after changing the theme ByName returned, its name is %q, want %q", again.Name, first.Name)
	}
}

func TestCurrent_DefaultAndSet(t *testing.T) {
	start := Current
	t.Cleanup(func() { Set(start) })

	if Current.Slug != "solarized-dark" {
		t.Errorf("the theme at startup is %q, want solarized-dark, the default the README names", Current.Slug)
	}
	if def := ByName("solarized-dark"); def == nil || *def != Current {
		t.Errorf("the theme at startup is not the built-in solarized-dark: %+v", Current)
	}
	if IndexOf(Current.Slug) != 0 {
		t.Errorf("the default theme is at index %d, want it first in the list", IndexOf(Current.Slug))
	}

	other := ByName("gruvbox-light")
	if other == nil {
		t.Fatal("fixture: gruvbox-light is not a built-in theme")
	}
	Set(*other)
	if Current != *other {
		t.Errorf("after Set, Current is %q, want %q", Current.Slug, other.Slug)
	}
	if !Current.Light || Current.Base00 == start.Base00 {
		t.Errorf("after Set, Current still has the old theme's values: light=%v Base00=%q", Current.Light, Current.Base00)
	}
}

func TestSparkGradient(t *testing.T) {
	dark, light := ByName("solarized-dark"), ByName("solarized-light")
	if dark == nil || light == nil || dark.Light || !light.Light {
		t.Fatal("fixture: solarized-dark and solarized-light are not a dark and a light theme")
	}
	d, l := dark.SparkGradient(), light.SparkGradient()
	for i := range d {
		if !hexColor.MatchString(string(d[i])) || !hexColor.MatchString(string(l[i])) {
			t.Errorf("stop %d: %q (dark), %q (light), want #rrggbb", i, d[i], l[i])
		}
	}
	if d == l {
		t.Error("a light theme and a dark theme have the same gradient")
	}
	// Eight different colors, green first and red last.
	seen := make(map[lipgloss.Color]bool)
	for _, c := range d {
		seen[c] = true
	}
	if len(seen) < 7 {
		t.Errorf("the dark gradient has %d different colors in 8 stops: %v", len(seen), d)
	}
	channel := func(c lipgloss.Color, at int) string { return string(c)[at : at+2] }
	if first, last := d[0], d[7]; channel(first, 3) <= channel(first, 1) || channel(last, 1) <= channel(last, 3) {
		t.Errorf("the dark gradient runs from %q to %q, want green to red", first, last)
	}
	// The gradient depends on light or dark only, not on the palette.
	if other := ByName("dracula"); other == nil || other.Light || other.SparkGradient() != d {
		t.Error("two dark themes have different gradients")
	}
}

func TestColorHelpers(t *testing.T) {
	for _, tc := range []struct {
		in   uint8
		want string
	}{{0, "00"}, {9, "09"}, {15, "0f"}, {16, "10"}, {255, "ff"}} {
		if got := byteToHex(tc.in); got != tc.want {
			t.Errorf("byteToHex(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, tc := range []struct{ in, want float64 }{{-0.5, 0}, {0, 0}, {0.25, 0.25}, {1, 1}, {1.5, 1}} {
		if got := clamp01(tc.in); got != tc.want {
			t.Errorf("clamp01(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
