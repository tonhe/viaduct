package tui

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/tonhe/viaduct/internal/hop"
)

// Narrow terminals with full-length IPv6 addresses (viaduct#58). The IPv6
// design accepted that optional columns are dropped below ~80 columns; these
// tests pin that the result stays readable: no address is cut mid-field, no
// row overflows, and hops remain distinguishable.

// Full-length IPv6 addresses: 8 groups of 4 hex digits, 39 characters, from
// the RFC 3849 documentation prefix. Hops 1/2 and 3/4 differ only in the last
// character, so a prefix-only truncation makes them look identical.
var longV6Hops = []string{
	"2001:db8:1111:2222:3333:4444:5555:6661",
	"2001:db8:1111:2222:3333:4444:5555:6662",
	"2001:db8:aaaa:bbbb:cccc:dddd:eeee:fff3",
	"2001:db8:aaaa:bbbb:cccc:dddd:eeee:fff4",
}

// hopsIPv6Long seeds session idx with four hops whose addresses are 39
// characters long, one closed round each.
func hopsIPv6Long(idx int) testOpt {
	return func(m *Model) {
		for i, a := range longV6Hops {
			h := m.sessions[idx].table.GetOrCreate(i + 1)
			h.MarkRoundSent()
			h.AddSample(net.ParseIP(a), time.Duration(5+i*5)*time.Millisecond, 0, hop.KindPrimary)
			h.MarkRoundEnd()
		}
	}
}

var narrowWidths = []int{60, 72, 80}

func TestNarrowIPv6_SingleTargetTable(t *testing.T) {
	setTestTheme(t)
	for _, w := range narrowWidths {
		t.Run(fmt.Sprintf("%d columns", w), func(t *testing.T) {
			m := newTestModel(withSize(w, 20), withIPVersion(6), hopsIPv6Long(0))
			lines := strings.Split(stripANSI(m.View()), "\n")
			for i, addr := range longV6Hops {
				row := ""
				for _, l := range lines {
					if strings.Contains(l, addr) {
						row = l
					}
				}
				if row == "" {
					t.Errorf("hop %d: full address %s not shown", i+1, addr)
					continue
				}
				if got := lipgloss.Width(row); got > w {
					t.Errorf("hop %d: row is %d columns wide, terminal is %d", i+1, got, w)
				}
				// The address is a whole field: a space on each side, then the loss column.
				if !strings.Contains(row, " "+addr+" ") {
					t.Errorf("hop %d: address runs into a neighbouring column: %q", i+1, row)
				}
				if !strings.Contains(row, "0.0%") {
					t.Errorf("hop %d: loss column missing: %q", i+1, row)
				}
			}
		})
	}
}

// paneIPCells returns, per hop, the IP cell text of the first pane in a
// rendered column view: the text between the hop number and the next column.
func paneIPCells(t *testing.T, view string, hops int) []string {
	t.Helper()
	cells := make([]string, hops)
	for _, l := range strings.Split(stripANSI(view), "\n") {
		if !strings.HasPrefix(l, "│") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(l, "│"))
		if len(fields) < 2 {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(fields[0], "%d", &n); err != nil || n < 1 || n > hops {
			continue
		}
		cells[n-1] = fields[1]
	}
	return cells
}

func TestNarrowIPv6_ColumnView(t *testing.T) {
	setTestTheme(t)
	for _, w := range narrowWidths {
		t.Run(fmt.Sprintf("%d columns", w), func(t *testing.T) {
			m := multiTestModel(t, withSize(w, 20), withIPVersion(6), hopsIPv6Long(0))
			hopsIPv6Long(1)(&m)
			m.columnView = true
			view := m.View()

			// Pane lines fill the terminal exactly: nothing overflows or collapses.
			for _, l := range strings.Split(stripANSI(view), "\n") {
				if strings.HasPrefix(l, "│") || strings.HasPrefix(l, "╭") || strings.HasPrefix(l, "╰") {
					if got := lipgloss.Width(l); got != w {
						t.Errorf("pane line is %d columns wide, want %d: %q", got, w, l)
					}
				}
			}

			cells := paneIPCells(t, view, len(longV6Hops))
			for i, addr := range longV6Hops {
				cell := cells[i]
				if cell == "" {
					t.Errorf("hop %d: no IP cell found", i+1)
					continue
				}
				if cell == addr {
					continue // shown in full
				}
				// Shortened: both ends of the address are kept around one
				// ellipsis, never a bare prefix.
				parts := strings.Split(cell, "…")
				if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
					t.Errorf("hop %d: address is cut without keeping both ends: %q", i+1, cell)
					continue
				}
				if !strings.HasPrefix(addr, parts[0]) || !strings.HasSuffix(addr, parts[1]) {
					t.Errorf("hop %d: %q is not the head and tail of %s", i+1, cell, addr)
				}
			}
			// Hops whose addresses differ only at the end stay distinguishable.
			if cells[0] == cells[1] || cells[2] == cells[3] {
				t.Errorf("different hops render identically: %q", cells)
			}
		})
	}
}

// A setup notice from target resolution (for example an IPv4 fallback because
// there is no IPv6 route) is shown in the status bar for as long as the trace
// runs (viaduct#55).
func TestStatusBarShowsTargetNotice(t *testing.T) {
	setTestTheme(t)
	cfg := newTestModel().sessions[0].probeCfg
	const notice = "no IPv6 route to 2001:db8:100::7; using IPv4"
	specs := []TargetSpec{{Name: "dual.example", IP: net.ParseIP("198.51.100.7"), Cfg: cfg, ProtocolName: "udp", Notice: notice}}
	m := NewMulti(specs, "0.0.0-test", true, 5, 0, 3, true, nil)
	m.width, m.height = 200, 20
	if !strings.Contains(stripANSI(m.View()), notice) {
		t.Errorf("status bar does not show the notice %q:\n%s", notice, stripANSI(m.View()))
	}

	plain := NewMulti([]TargetSpec{{Name: "v4.example", IP: net.ParseIP("198.51.100.7"), Cfg: cfg, ProtocolName: "udp"}}, "0.0.0-test", true, 5, 0, 3, true, nil)
	plain.width, plain.height = 200, 20
	if strings.Contains(stripANSI(plain.View()), "no IPv6 route") {
		t.Error("a notice is shown for a target that has none")
	}
}

func TestTruncateAddr(t *testing.T) {
	const v6 = "2001:db8:1111:2222:3333:4444:5555:6661"
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"192.0.2.1", 15, "192.0.2.1"},        // fits
		{"203.0.113.254", 13, "203.0.113.254"}, // exactly fits
		{v6, 39, v6},                           // exactly fits
		{v6, 15, "2001:db…55:6661"},            // 7 + ellipsis + 7
		{v6, 14, "2001:db…5:6661"},             // 7 + ellipsis + 6: the head gets the odd one
		{v6, 9, "2001…6661"},
		{v6, 3, "2…1"},
		{v6, 2, "2…"},
		{v6, 1, "…"},
		{v6, 0, ""},
	} {
		got := truncateAddr(tc.in, tc.n)
		if got != tc.want {
			t.Errorf("truncateAddr(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
		if w := lipgloss.Width(got); w > tc.n {
			t.Errorf("truncateAddr(%q, %d) is %d columns wide", tc.in, tc.n, w)
		}
	}
}

// IPv4 addresses never needed the extra width; the layout for them is unchanged.
func TestComputePaneLayout_IPv4Unchanged(t *testing.T) {
	for inner := 10; inner <= 120; inner++ {
		short := computePaneLayout(inner, len("192.0.2.1"))
		longest := computePaneLayout(inner, len("203.0.113.254"))
		if short != longest {
			t.Fatalf("inner %d: layout depends on IPv4 address length: %+v vs %+v", inner, short, longest)
		}
		if short.ipw > paneIPMax {
			t.Fatalf("inner %d: IPv4 IP column is %d wide, max is %d", inner, short.ipw, paneIPMax)
		}
	}
}

// A long address takes the room it needs before the hostname column gets any.
func TestComputePaneLayout_LongAddressGetsRoomBeforeHostname(t *testing.T) {
	lay := computePaneLayout(80, 39)
	if lay.ipw != 39 {
		t.Errorf("IP column = %d wide in an 80-wide pane, want 39", lay.ipw)
	}
	if !lay.showAvg || !lay.showLoss {
		t.Errorf("Avg/Loss dropped in an 80-wide pane: %+v", lay)
	}
	tight := computePaneLayout(40, 39)
	if tight.hostw != 0 {
		t.Errorf("hostname column shown (%d wide) while the address is still cut to %d", tight.hostw, tight.ipw)
	}
}
