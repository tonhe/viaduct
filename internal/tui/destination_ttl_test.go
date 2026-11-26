package tui

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tonhe/viaduct/internal/dest"
	"github.com/tonhe/viaduct/internal/export"
	"github.com/tonhe/viaduct/internal/hop"
	"github.com/tonhe/viaduct/internal/probe"
)

// Tests for which TTL is the destination's when the target answers at more
// than one (viaduct#234). They feed the model the messages a trace produces:
// what was sent, each reply, each echo, each round's end.

const destSimTarget = "192.0.2.1" // newTestModel's target

// simProbe is one TTL-limited probe of a round: its TTL, type and flow, and
// the address that answered it ("" when nothing did).
type simProbe struct {
	ttl  int
	pt   probe.ProbeType
	flow int
	from string
}

// destSim drives one session of a test model round by round.
type destSim struct {
	t    *testing.T
	m    Model
	sink *fakeEchoSink
	log  *debugSink
	// echoAnswered says whether a node answers direct echoes.
	echoAnswered func(ip string) bool
	echoTargets  []probe.EchoTarget // submitted when the last round closed
	rounds       int
}

func newDestSim(t *testing.T, opts ...testOpt) *destSim {
	s := &destSim{t: t, m: newTestModel(opts...), sink: &fakeEchoSink{}, log: &debugSink{}}
	s.m.sessions[0].echoSink = s.sink
	s.m.SetDebugLog(s.log.logf)
	s.echoAnswered = func(string) bool { return true }
	return s
}

func (s *destSim) sess() *TargetSession { return s.m.sessions[0] }

// round plays one round: the probes are sent, the answered ones are
// delivered, every echo target of the previous round gets an echo, and the
// round is closed.
func (s *destSim) round(probes []simProbe) {
	type counts struct{ icmp, primary int }
	sent := map[int]*counts{}
	for _, p := range probes {
		c := sent[p.ttl]
		if c == nil {
			c = &counts{}
			sent[p.ttl] = c
		}
		if p.pt == probe.ProbeICMPTTL {
			c.icmp++
		} else {
			c.primary++
		}
	}
	for ttl, c := range sent {
		s.sess().table.MarkRoundSentByType(ttl, c.icmp, c.primary)
	}
	for _, p := range probes {
		if p.from == "" {
			continue
		}
		s.m, _ = s.m.onHopUpdate(HopUpdateMsg{Result: probe.Result{
			TTL: p.ttl, IP: net.ParseIP(p.from), RTT: 10 * time.Millisecond,
			IsTarget: p.from == destSimTarget, FlowID: p.flow, ProbeType: p.pt,
		}})
	}
	for _, e := range s.echoTargets {
		s.m, _ = s.m.onEchoSent(EchoSentMsg{IP: e.IP, TTL: e.TTL})
		if s.echoAnswered(e.IP.String()) {
			s.m, _ = s.m.onHopUpdate(HopUpdateMsg{Result: probe.Result{
				TTL: e.TTL, IP: e.IP, RTT: 10 * time.Millisecond,
				IsTarget: e.IP.String() == destSimTarget, ProbeType: probe.ProbeICMPEcho,
			}})
		}
	}
	s.sink.submitted = nil
	s.m, _ = s.m.onRoundEnd(RoundEndMsg{})
	s.echoTargets = s.sink.submitted
	s.rounds++
}

// report is the export of the session, built as exportCmd builds it.
func (s *destSim) report() export.Report {
	sess := s.sess()
	maxTTL := sess.table.MaxTTLSeen()
	if sess.maxTTLHit > 0 && sess.maxTTLHit < maxTTL {
		maxTTL = sess.maxTTLHit
	}
	return export.BuildReport("test", sess.target, sess.targetIP, sess.protocolName, sess.table, nil, nil, maxTTL)
}

// wantDestination checks that every output puts the destination at ttl: the
// session (which the TUI views read), the hop table (report mode and the
// debug log), the hops shown, the echo targets, the exports and the debug
// log's final snapshot.
func (s *destSim) wantDestination(ttl int) {
	s.t.Helper()
	sess := s.sess()
	if sess.maxTTLHit != ttl {
		s.t.Errorf("round %d: session destination TTL = %d, want %d", s.rounds, sess.maxTTLHit, ttl)
	}
	if got := sess.table.TargetTTL(); got != ttl {
		s.t.Errorf("round %d: table destination TTL = %d, want %d", s.rounds, got, ttl)
	}
	if got := summaryHops(sess); got != ttl {
		s.t.Errorf("round %d: hops shown = %d, want %d", s.rounds, got, ttl)
	}
	last := 0
	for _, e := range s.echoTargets {
		if e.TTL > last {
			last = e.TTL
		}
	}
	if last != ttl {
		s.t.Errorf("round %d: echo targets reach TTL %d, want %d", s.rounds, last, ttl)
	}

	r := s.report()
	if len(r.Hops) != ttl || r.Hops[len(r.Hops)-1].TTL != ttl {
		s.t.Errorf("round %d: export has %d hops, want the last to be TTL %d", s.rounds, len(r.Hops), ttl)
	}
	var js, csv, dot bytes.Buffer
	if err := export.WriteJSON(&js, r); err != nil {
		s.t.Fatal(err)
	}
	if err := export.WriteCSV(&csv, r); err != nil {
		s.t.Fatal(err)
	}
	if err := export.WriteDOT(&dot, r); err != nil {
		s.t.Fatal(err)
	}
	if strings.Contains(js.String(), fmt.Sprintf(`"ttl": %d,`, ttl+1)) || !strings.Contains(js.String(), fmt.Sprintf(`"ttl": %d,`, ttl)) {
		s.t.Errorf("round %d: JSON export does not end at TTL %d", s.rounds, ttl)
	}
	if strings.Contains(csv.String(), fmt.Sprintf("\n%d,", ttl+1)) || !strings.Contains(csv.String(), fmt.Sprintf("\n%d,", ttl)) {
		s.t.Errorf("round %d: CSV export does not end at TTL %d", s.rounds, ttl)
	}
	// DOT names a node ttlN_<address> and a silent hop gap_N.
	d := dot.String()
	if strings.Contains(d, fmt.Sprintf("ttl%d_", ttl+1)) || strings.Contains(d, fmt.Sprintf("gap_%d ", ttl+1)) ||
		!strings.Contains(d, fmt.Sprintf("ttl%d_%s ", ttl, strings.ReplaceAll(destSimTarget, ".", "_"))) {
		s.t.Errorf("round %d: DOT export does not end on the target at TTL %d", s.rounds, ttl)
	}

	mark := len(s.log.lines)
	s.m.DebugFinal()
	final := s.log.lines[mark:]
	s.log.lines = s.log.lines[:mark]
	for _, line := range final {
		var lineTTL int
		if i := strings.Index(line, " ttl="); i < 0 {
			continue
		} else if _, err := fmt.Sscanf(line[i:], " ttl=%d", &lineTTL); err != nil {
			s.t.Fatalf("debug log line %q: %v", line, err)
		}
		beyond := strings.HasSuffix(line, " beyond=yes")
		if beyond != (lineTTL > ttl) {
			s.t.Errorf("round %d: debug log TTL %d beyond=%v, want the destination at TTL %d: %q", s.rounds, lineTTL, beyond, ttl, line)
		}
	}
}

func router(ttl int) string { return fmt.Sprintf("198.51.100.%d", ttl) }

// One flow (ICMP mode). Routers answer at TTL 1 to 12, TTL 13 to 24 are
// silent and the target answers at TTL 25. In the first round one probe sent
// with TTL 21 reaches the target as well. That reply used to pin the
// destination row at TTL 21 for good, where nearly every later probe is lost
// while the direct echoes are answered: 100% loss, then "rate-limited".
func TestDestination_StrayReplyBelowTheUsualDistance(t *testing.T) {
	s := newDestSim(t)
	// icmpRound probes TTL 1 to upTo; the target is at TTL 25.
	icmpRound := func(upTo int, strayAt int) {
		var ps []simProbe
		for ttl := 1; ttl <= upTo; ttl++ {
			p := simProbe{ttl: ttl, pt: probe.ProbeICMPTTL}
			switch {
			case ttl <= 12:
				p.from = router(ttl)
			case ttl >= 25 || ttl == strayAt:
				p.from = destSimTarget
			}
			ps = append(ps, p)
		}
		s.round(ps)
	}

	icmpRound(30, 21)
	s.wantDestination(21) // the first round settles it at once, as before

	// The tracer looks one TTL further each round the target is silent.
	for upTo := 21; upTo <= 24; upTo++ {
		icmpRound(upTo, 0)
		s.wantDestination(21)
	}
	// It finds the target at TTL 25; the destination follows after
	// dest.MoveRounds rounds.
	for i := 1; i < dest.MoveRounds; i++ {
		icmpRound(25, 0)
		s.wantDestination(21)
	}
	icmpRound(25, 0)
	s.wantDestination(25)

	for s.rounds < 40 {
		icmpRound(25, 0)
	}
	s.wantDestination(25)

	h := s.sess().table.Get(25)
	if pct, kind := h.Loss(); pct != 0 || kind != hop.LossMeasured {
		t.Errorf("destination row (TTL 25): loss %s, want 0.0%% measured", hop.FormatLoss(pct, kind))
	}
	if got := h.RateLimitState(); got != hop.RateLimitNo {
		t.Errorf("destination row (TTL 25): rate-limit verdict %v, want no", got)
	}
	hops := s.report().Hops
	if len(hops) != 25 {
		t.Fatalf("export has %d hops, want 25", len(hops))
	}
	last := hops[24]
	if last.LossKind != "measured" || last.HeadlineLoss != 0 || len(last.Nodes) != 1 || last.Nodes[0].IP != destSimTarget {
		t.Errorf("export destination hop = %+v, want the target with 0%% measured loss", last)
	}

	// TTL 21 is an ordinary hop that lists the target: one answer in 40
	// rounds. It is not the destination and is judged on its own.
	h21 := s.sess().table.Get(21)
	if sent, recvd := h21.ProbeCounts(); sent != 40 || recvd != 1 {
		t.Errorf("TTL 21: probes sent/received = %d/%d, want 40/1", sent, recvd)
	}
}

// Six UDP flows and the ICMP TTL probe. The target answers flow 4 at TTL 8
// and every flow at TTL 9; at TTL 8 routers answer the other probes, and not
// all of them. The table used to end at TTL 8, on a router's loss figure.
func TestDestination_FlowsOfUnequalPathLength(t *testing.T) {
	s := newDestSim(t)
	udpRound := func(upTo int) {
		var ps []simProbe
		for ttl := 1; ttl <= upTo; ttl++ {
			for i := -1; i < 6; i++ {
				p := simProbe{ttl: ttl, pt: probe.ProbeUDPTTL, flow: i}
				if i < 0 {
					p = simProbe{ttl: ttl, pt: probe.ProbeICMPTTL}
				}
				switch {
				case ttl <= 7:
					p.from = router(ttl)
				case ttl >= 9, i == 4:
					p.from = destSimTarget
				case i < 0, i == 0:
					p.from = "198.51.100.81" // the ICMP probe and flow 0
				case i == 1:
					p.from = "198.51.100.82"
				default:
					// flows 2, 3 and 5 get no answer at TTL 8
				}
				ps = append(ps, p)
			}
		}
		s.round(ps)
	}

	udpRound(12)
	s.wantDestination(9)
	for s.rounds < 30 {
		udpRound(9)
		s.wantDestination(9)
	}

	h9 := s.sess().table.Get(9)
	if pct, kind := h9.Loss(); pct != 0 || kind != hop.LossMeasured {
		t.Errorf("destination row (TTL 9): loss %s, want 0.0%% measured", hop.FormatLoss(pct, kind))
	}
	if pn := h9.PrimaryNode(); pn == nil || pn.GetIP().String() != destSimTarget || len(h9.GetNodes()) != 1 {
		t.Errorf("destination row (TTL 9): want the target as its only node, got %d nodes", len(h9.GetNodes()))
	}

	// TTL 8 is an intermediate hop: a router is its headline node, the
	// target is listed among its nodes, and its loss is its own (3 of 7
	// probes a round unanswered).
	h8 := s.sess().table.Get(8)
	if pn := h8.PrimaryNode(); pn == nil || pn.GetIP().String() != "198.51.100.81" {
		t.Errorf("TTL 8: primary node = %v, want the router 198.51.100.81", pn.GetIP())
	}
	listed := false
	for _, n := range h8.GetNodes() {
		listed = listed || n.GetIP().String() == destSimTarget
	}
	if !listed {
		t.Error("TTL 8: the target is not listed among its nodes")
	}
	if sent, recvd := h8.ProbeCounts(); sent != 30*7 || recvd != 30*4 {
		t.Errorf("TTL 8: probes sent/received = %d/%d, want %d/%d", sent, recvd, 30*7, 30*4)
	}
	r := s.report()
	if len(r.Hops) != 9 || len(r.Hops[8].Nodes) == 0 {
		t.Fatalf("export has %d hops, want 9 with the target at the last", len(r.Hops))
	}
	if got := r.Hops[8].Nodes[0].IP; got != destSimTarget {
		t.Errorf("export: last hop's first node = %s, want the target", got)
	}
	if got := r.Hops[8].HeadlineLoss; got != 0 {
		t.Errorf("export: destination headline loss = %.1f, want 0", got)
	}
}

// The path gets shorter: the target, at TTL 6, starts answering at TTL 5.
// The destination moves after dest.MoveRounds rounds. The target ignores
// ping, so its loss is measured and can alert: it stops answering for a
// while at TTL 6 and the alert is raised. On the move the alert state starts
// over at the new row, and each hop keeps its own counts and verdict.
func TestDestination_PathGetsShorter(t *testing.T) {
	s := newDestSim(t, withAlertEngine(5, 0, 3))
	s.echoAnswered = func(ip string) bool { return ip != destSimTarget }
	// icmpRound probes TTL 1 to upTo with the target at TTL dist.
	icmpRound := func(upTo, dist int, answers bool) {
		var ps []simProbe
		for ttl := 1; ttl <= upTo; ttl++ {
			p := simProbe{ttl: ttl, pt: probe.ProbeICMPTTL, from: router(ttl)}
			if ttl >= dist {
				p.from = ""
				if answers {
					p.from = destSimTarget
				}
			}
			ps = append(ps, p)
		}
		s.round(ps)
	}
	engine := s.sess().alertEngine

	// 25 healthy rounds: more than the 20 echoes the target must have
	// ignored before its loss counts as measured.
	icmpRound(10, 6, true)
	for i := 1; i < 25; i++ {
		icmpRound(6, 6, true)
	}
	s.wantDestination(6)
	// The target goes silent. The tracer looks one TTL further each round;
	// the destination row stays where it is and its loss raises the alert.
	for i := 0; i < 4; i++ {
		icmpRound(6+i, 6, false)
		s.wantDestination(6)
	}
	if engine.State() != AlertDegraded {
		t.Fatalf("alert state = %v after 4 lost rounds at the destination, want degraded", engine.State())
	}
	sent6, recvd6 := s.sess().table.Get(6).ProbeCounts()

	// It answers again, one hop closer.
	for i := 1; i < dest.MoveRounds; i++ {
		icmpRound(10, 5, true)
		s.wantDestination(6)
		if engine.State() != AlertDegraded {
			t.Fatalf("alert cleared before the destination moved (round %d)", s.rounds)
		}
	}
	icmpRound(10, 5, true)
	s.wantDestination(5)

	// The alert state starts over at the new row.
	if engine.State() != AlertHealthy || engine.consecutiveBad != 0 {
		t.Errorf("alert after the move: state %v, %d consecutive bad rounds; want healthy and 0", engine.State(), engine.consecutiveBad)
	}
	// The row that was the destination keeps its counts and is hidden.
	if sent, recvd := s.sess().table.Get(6).ProbeCounts(); sent != sent6+dest.MoveRounds || recvd != recvd6+dest.MoveRounds {
		t.Errorf("TTL 6 after the move: sent/received = %d/%d, want %d/%d", sent, recvd, sent6+dest.MoveRounds, recvd6+dest.MoveRounds)
	}
	// The new row is judged on its own probes: the router answered every
	// one before the path changed and the target every one since.
	h5 := s.sess().table.Get(5)
	if pct, kind := h5.Loss(); pct != 0 || kind != hop.LossMeasured {
		t.Errorf("new destination row (TTL 5): loss %s, want 0.0%% measured", hop.FormatLoss(pct, kind))
	}

	for i := 0; i < 5; i++ {
		icmpRound(5, 5, true)
		s.wantDestination(5)
	}
	if engine.State() != AlertHealthy {
		t.Errorf("alert state = %v on a healthy path, want healthy", engine.State())
	}
}

// The alert engine counts consecutive bad rounds at the destination's hop.
// When the destination moves, the rounds counted were another hop's: the
// count and a raised alert are dropped, and the new hop has to earn its own.
func TestAlertEngine_StartsOverWhenTheDestinationMoves(t *testing.T) {
	table := hop.NewTable(30)
	ip := net.ParseIP("192.0.2.9")
	// Both hops have answered a probe, and neither has answered any of 20
	// echoes: their loss is measured and counts toward alerting.
	for _, ttl := range []int{4, 5} {
		table.MarkRoundSentByType(ttl, 1, 0)
		table.Get(ttl).AddSample(ip, time.Millisecond, 0, hop.KindICMPTTL)
		for i := 0; i < 20; i++ {
			table.Get(ttl).GetNodes()[0].MarkEchoSent()
		}
	}
	CloseRound(table)
	// lostRound sends one probe to TTL 4 and to TTL 5 and neither answers.
	lostRound := func(e *AlertEngine, destTTL int) {
		table.MarkRoundSentByType(4, 1, 0)
		table.MarkRoundSentByType(5, 1, 0)
		e.ObserveRound(table.Snapshot(), CloseRound(table), destTTL)
	}

	e := NewAlertEngine(5, 0, 3)
	lostRound(e, 5)
	lostRound(e, 5)
	lostRound(e, 4) // the destination moved: this is the first bad round at TTL 4
	if e.State() != AlertHealthy || e.consecutiveBad != 1 {
		t.Fatalf("after a move: state %v, %d consecutive bad rounds; want healthy and 1", e.State(), e.consecutiveBad)
	}
	lostRound(e, 4)
	lostRound(e, 4)
	if e.State() != AlertDegraded {
		t.Fatalf("state %v after 3 bad rounds at the new destination, want degraded", e.State())
	}
	lostRound(e, 5) // and back: the raised alert is dropped with the count
	if e.State() != AlertHealthy || e.consecutiveBad != 1 {
		t.Errorf("after moving back: state %v, %d consecutive bad rounds; want healthy and 1", e.State(), e.consecutiveBad)
	}
}
