package hop

import (
	"net"
	"reflect"
	"testing"
	"time"
	"unsafe"
)

// ---------------------------------------------------------------------------
// PathNode.Reset: the whole state
// ---------------------------------------------------------------------------

// nodeFields calls fn with the name and an addressable, settable value of
// every field of pn except its mutex. PathNode's fields are unexported, so the
// values are reached through their addresses.
func nodeFields(pn *PathNode, fn func(name string, v reflect.Value)) {
	v := reflect.ValueOf(pn).Elem()
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		if name == "mu" {
			continue
		}
		f := v.Field(i)
		fn(name, reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem())
	}
}

// fillNonZero sets v, and everything inside it, to a value that is not its
// zero value and not the value a new node has.
func fillNonZero(t *testing.T, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(3)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7.5)
	case reflect.String:
		v.SetString("set")
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			fillNonZero(t, v.Index(i))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			fillNonZero(t, reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem())
		}
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key := reflect.New(v.Type().Key()).Elem()
		fillNonZero(t, key)
		m.SetMapIndex(key, reflect.New(v.Type().Elem()).Elem())
		v.Set(m)
	default:
		t.Fatalf("fillNonZero: no rule for a %s; add one so the field is covered", v.Kind())
	}
}

// Reset returns a node to the state of a new node for the same address: every
// field, not only the counters a caller can read. Each field is first given a
// value a new node does not have, so a field Reset leaves alone, or sets to
// anything but its starting value, is named in the failure. The fields are
// found by reflection: one added later is covered without a change here.
func TestPathNodeReset_EveryFieldReturnsToItsStartingValue(t *testing.T) {
	ip := net.ParseIP("192.0.2.10")
	pn := NewPathNode(ip)
	fresh := NewPathNode(ip)

	nodeFields(pn, func(name string, v reflect.Value) {
		if name == "IP" {
			return // the one thing Reset keeps
		}
		fillNonZero(t, v)
	})
	// The fill did change every field: otherwise the comparison below proves
	// nothing for it.
	want := map[string]any{}
	nodeFields(fresh, func(name string, v reflect.Value) { want[name] = v.Interface() })
	nodeFields(pn, func(name string, v reflect.Value) {
		if name != "IP" && reflect.DeepEqual(v.Interface(), want[name]) {
			t.Fatalf("field %s was not changed by the fill, so Reset is not tested for it", name)
		}
	})

	pn.Reset()

	nodeFields(pn, func(name string, v reflect.Value) {
		if got := v.Interface(); !reflect.DeepEqual(got, want[name]) {
			t.Errorf("after Reset, %s = %#v, want %#v as in a new node", name, got, want[name])
		}
	})
	if !pn.IP.Equal(ip) {
		t.Errorf("after Reset, IP = %v, want %v: Reset keeps the address", pn.IP, ip)
	}
}

// use drives a node through everything a trace does to one: replies of every
// probe type on two flows, echoes sent and answered, rounds closed, a
// hostname, an ASN and a verdict.
func use(pn *PathNode) {
	pn.SetHostname("host.example")
	pn.SetASN(64500, "EXAMPLE-NET")
	for round := 0; round < sparklineSize+3; round++ {
		rtt := time.Duration(10+round) * time.Millisecond
		if round%4 != 3 { // every fourth round the TTL probes go unanswered
			pn.AddSample(rtt, round%2, KindICMPTTL)
			pn.AddSample(rtt+time.Millisecond, round%2, KindPrimary)
		}
		if round%2 == 0 {
			pn.MarkEchoSent()
			pn.AddSample(rtt, 0, KindEcho)
		}
		pn.markProbesSent(1, 2)
		pn.setHopVerdict(RateLimitYes)
		pn.RecordRoundAvg()
		pn.AdvanceRound()
		pn.MarkProbeRoundEnd()
	}
	// The round in progress: a reply, and an echo not yet answered.
	pn.AddSample(50*time.Millisecond, 1, KindPrimary)
	pn.MarkEchoSent()
}

// A node that was used, reset and used again holds exactly what a new node
// holds after the same use: nothing of its earlier life is left to change a
// later figure. This reaches the per-probe statistics, the sparkline samples
// and the echo flag through the methods a trace calls.
func TestPathNodeReset_AReusedNodeMatchesANewOne(t *testing.T) {
	ip := net.ParseIP("192.0.2.11")

	reused := NewPathNode(ip)
	use(reused)
	reused.Reset()

	// Straight after Reset the readings are those of a node that has seen nothing.
	if got := reused.SparklineData(); len(got) != 0 {
		t.Errorf("after Reset the sparkline has %d samples, want none: %v", len(got), got)
	}
	for name, s := range map[string]ProbeStats{
		"ICMP TTL": reused.ICMPTTLStats(), "primary": reused.PrimaryStats(), "echo": reused.EchoStats(),
	} {
		if s != (ProbeStats{}) {
			t.Errorf("after Reset the %s statistics are %+v, want all zero", name, s)
		}
	}
	if reused.GetReceived() != 0 || reused.AvgRTT() != 0 || reused.GetMinRTT() != 0 || reused.GetMaxRTT() != 0 ||
		reused.GetLastRTT() != 0 || reused.Jitter() != 0 || reused.JitterMean() != 0 || reused.GeoMean() != 0 || reused.StDev() != 0 {
		t.Errorf("after Reset: received %d, avg %v, best %v, worst %v, last %v, jitter %v (mean %v), geometric mean %v, deviation %v; want all zero",
			reused.GetReceived(), reused.AvgRTT(), reused.GetMinRTT(), reused.GetMaxRTT(), reused.GetLastRTT(),
			reused.Jitter(), reused.JitterMean(), reused.GeoMean(), reused.StDev())
	}
	if reused.Trend() != "stable" || reused.StabilityPercent() != 0 || len(reused.GetFlowIDs()) != 0 {
		t.Errorf("after Reset: trend %q, stability %.0f%%, flows %v; want stable, 0%%, none",
			reused.Trend(), reused.StabilityPercent(), reused.GetFlowIDs())
	}
	if name, asn := reused.GetHostname(), func() int { n, _ := reused.GetASN(); return n }(); name != "" || asn != 0 {
		t.Errorf("after Reset: hostname %q, ASN %d; want none", name, asn)
	}

	use(reused)
	fresh := NewPathNode(ip)
	use(fresh)

	want := map[string]any{}
	nodeFields(fresh, func(name string, v reflect.Value) { want[name] = v.Interface() })
	nodeFields(reused, func(name string, v reflect.Value) {
		if got := v.Interface(); !reflect.DeepEqual(got, want[name]) {
			t.Errorf("reused node: %s = %#v, want %#v as in a new node after the same use", name, got, want[name])
		}
	})
}

// ---------------------------------------------------------------------------
// PathNode.Trend
// ---------------------------------------------------------------------------

// nodeWithRoundAverages returns a node that has closed one answered round for
// each value, in order, with that round average in milliseconds.
func nodeWithRoundAverages(ms ...float64) *PathNode {
	pn := NewPathNode(net.ParseIP("192.0.2.20"))
	for _, v := range ms {
		pn.AddSample(time.Duration(v*float64(time.Millisecond)), 0, KindPrimary)
		pn.RecordRoundAvg()
	}
	return pn
}

// rising returns n values starting at from, each step higher than the last.
func rising(from, step float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = from + step*float64(i)
	}
	return out
}

// Trend fits a line through the last ten round averages and compares its
// slope, in milliseconds a round, with a threshold: 0.5 ms, or 1% of the
// average when that is more. The slope must be past the threshold, not at it.
func TestPathNodeTrend(t *testing.T) {
	cases := []struct {
		name string
		avgs []float64 // round averages in ms, oldest first
		want string
	}{
		// Fewer than three rounds say nothing, however steep.
		{"no round", nil, "stable"},
		{"one round", []float64{10}, "stable"},
		{"two rounds, steeply up", []float64{10, 100}, "stable"},
		{"two rounds, steeply down", []float64{100, 10}, "stable"},
		{"three rounds, steeply up: the first count that is judged", []float64{10, 55, 100}, "degrading"},
		{"three rounds, steeply down", []float64{100, 55, 10}, "improving"},

		// A window that is not full yet is read from its first entry.
		{"five rounds up", rising(10, 2, 5), "degrading"},
		{"five rounds down", rising(30, -2, 5), "improving"},
		{"nine rounds up", rising(10, 2, 9), "degrading"},
		{"exactly ten rounds up", rising(10, 2, 10), "degrading"},

		// A full window is read oldest first, from wherever the ring's next
		// write falls. The three oldest rounds here have been overwritten;
		// read from the ring's first slot the last ten would look flat.
		{"thirteen rounds: the last ten rise", append([]float64{40, 40, 40}, rising(10, 1, 10)...), "degrading"},
		{"thirteen rounds: the last ten fall", append([]float64{1, 1, 1}, rising(30, -1, 10)...), "improving"},
		// Only the last ten count: an early climb that has left the window.
		{"an old climb, then ten flat rounds", append(rising(10, 5, 5), rising(40, 0, 10)...), "stable"},

		// The 0.5 ms threshold, for averages of 50 ms and under.
		{"slope 0.25 ms a round", []float64{10, 10.25, 10.5}, "stable"},
		{"slope exactly 0.5: at the threshold, not past it", []float64{10, 10.5, 11}, "stable"},
		{"slope 0.55: just past it", []float64{10, 10.55, 11.1}, "degrading"},
		{"slope 0.75", []float64{10, 10.75, 11.5}, "degrading"},
		{"slope exactly -0.5", []float64{11, 10.5, 10}, "stable"},
		{"slope -0.55", []float64{11.1, 10.55, 10}, "improving"},
		{"slope -0.75", []float64{11.5, 10.75, 10}, "improving"},
		{"flat", []float64{10, 10, 10, 10}, "stable"},

		// Above 50 ms the threshold is 1% of the average: 2 ms at 200 ms.
		{"at 200 ms, slope 1: past 0.5 but under 1%", []float64{199, 200, 201}, "stable"},
		{"at 200 ms, slope exactly 2: at the threshold", []float64{198, 200, 202}, "stable"},
		{"at 200 ms, slope 3", []float64{197, 200, 203}, "degrading"},
		{"at 200 ms, slope -1", []float64{201, 200, 199}, "stable"},
		{"at 200 ms, slope exactly -2", []float64{202, 200, 198}, "stable"},
		{"at 200 ms, slope -3", []float64{203, 200, 197}, "improving"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nodeWithRoundAverages(tc.avgs...).Trend(); got != tc.want {
				t.Errorf("round averages %v ms: Trend() = %q, want %q", tc.avgs, got, tc.want)
			}
		})
	}
}

// A round in which nothing answered adds no point to the trend: two answered
// rounds are still too few however many silent rounds follow, and a silent
// round between answered ones does not flatten the line.
func TestPathNodeTrend_SilentRoundsAddNoPoint(t *testing.T) {
	pn := nodeWithRoundAverages(10, 100)
	for i := 0; i < 5; i++ {
		pn.RecordRoundAvg() // nothing answered this round
	}
	if got := pn.Trend(); got != "stable" {
		t.Errorf("two answered rounds and five silent ones: Trend() = %q, want stable (fewer than three points)", got)
	}

	pn = nodeWithRoundAverages(10, 55)
	pn.RecordRoundAvg() // silent
	pn.AddSample(100*time.Millisecond, 0, KindPrimary)
	pn.RecordRoundAvg()
	if got := pn.Trend(); got != "degrading" {
		t.Errorf("10, 55, a silent round, 100: Trend() = %q, want degrading", got)
	}
}
