package tui

import (
	"bytes"
	"fmt"
	"math/rand"
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

	// The first round probes every TTL and settles on the lowest TTL the
	// target answered at. Nothing answers between TTL 21 and TTL 25, and a
	// gap in the target's replies is what lost replies look like, so that is
	// TTL 21.
	icmpRound(30, 21)
	s.wantDestination(21)
	// The tracer looks one TTL further each round the target is silent.
	for upTo := 21; upTo <= 24; upTo++ {
		icmpRound(upTo, 0)
		s.wantDestination(21)
	}
	// It finds the target at TTL 25. The target silent at the destination's
	// TTL and answering beyond it is also what loss looks like, so the row
	// follows after dest.SilentMoveRounds rounds answered there: round 29.
	for i := 1; i < dest.SilentMoveRounds; i++ {
		icmpRound(25, 0)
		s.wantDestination(21)
	}
	icmpRound(25, 0)
	s.wantDestination(25)
	if s.rounds != 5+dest.SilentMoveRounds {
		t.Fatalf("fixture: the move came in round %d, want %d", s.rounds, 5+dest.SilentMoveRounds)
	}

	for s.rounds < 60 {
		// Strays in later rounds, two rounds in a row: no move back.
		stray := 0
		if s.rounds == 40 || s.rounds == 41 {
			stray = 21
		}
		icmpRound(25, stray)
		s.wantDestination(25)
	}

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

	// TTL 21 is an ordinary hop that lists the target: three answers in 60
	// rounds. It is not the destination and is judged on its own.
	h21 := s.sess().table.Get(21)
	if sent, recvd := h21.ProbeCounts(); sent != 60 || recvd != 3 {
		t.Errorf("TTL 21: probes sent/received = %d/%d, want 60/3", sent, recvd)
	}
}

// The same stray reply on a path whose routers answer: TTL 1 to 24 are
// routers, the target is at TTL 25, and in round one the probe sent with
// TTL 21 reaches the target. The routers that answer at TTL 22 to 24 place
// the destination beyond them: TTL 25 when round one closes, and in every
// round after.
func TestDestination_StrayReplyBelowRoutersThatAnswer(t *testing.T) {
	s := newDestSim(t)
	for s.rounds < 5 {
		upTo := 25
		if s.rounds == 0 {
			upTo = 30
		}
		var ps []simProbe
		for ttl := 1; ttl <= upTo; ttl++ {
			p := simProbe{ttl: ttl, pt: probe.ProbeICMPTTL, from: router(ttl)}
			if ttl >= 25 || (s.rounds == 0 && ttl == 21) {
				p.from = destSimTarget
			}
			ps = append(ps, p)
		}
		s.round(ps)
		s.wantDestination(25)
	}
	if moved := s.log.containing("destination moved"); len(moved) != 0 {
		t.Errorf("debug log: %q, want no move", moved)
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
// while at TTL 6 and the alert is raised. The alert stays raised until the
// destination has moved: the new row has lost nothing, so its first round as
// the destination clears it. Each hop keeps its own counts and verdict.
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
	if moved := s.log.containing("destination moved from ttl=6 to ttl=5"); len(moved) != 1 {
		t.Errorf("debug log: %d lines for the move from TTL 6 to TTL 5, want 1", len(moved))
	}

	// The alert follows the destination to its new row, which has lost
	// nothing: this round was a good one, and it clears the alert.
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

// A destination with real loss: one flow, the target at TTL 6 loses half the
// probes that reach it and ignores ping, so its loss is measured and well
// over the alert threshold. The tracer probes further out after a round the
// target did not answer (played here by a dest.Tracker, as probe.Tracer has
// one), and the target answers some of those probes while the one at TTL 6
// is lost. That is what loss looks like, not a longer path: the destination
// row stays at TTL 6 in every output, the rows beyond it stay hidden and get
// no echoes, and the loss alert, once raised, stays raised.
func TestDestination_LossyDestinationKeepsItsRowAndItsAlert(t *testing.T) {
	const dist, rounds = 6, 300
	s := newDestSim(t, withAlertEngine(5, 0, 3))
	s.echoAnswered = func(ip string) bool { return ip != destSimTarget }
	engine := s.sess().alertEngine
	rnd := rand.New(rand.NewSource(1))
	var tracer dest.Tracker
	flow := dest.Flow{Kind: int(probe.ProbeICMPTTL), Aux: true}

	raisedAt, beyondProbed := 0, 0
	for s.rounds < rounds {
		upTo := tracer.Reach()
		if upTo == 0 {
			upTo = 30
		} else if upTo > dist {
			beyondProbed++
		}
		var ps []simProbe
		for ttl := 1; ttl <= upTo; ttl++ {
			p := simProbe{ttl: ttl, pt: probe.ProbeICMPTTL}
			switch {
			case ttl < dist:
				p.from = router(ttl)
				tracer.Router(flow, ttl)
			case s.rounds == 0 || rnd.Float64() >= 0.5: // round one is clean
				p.from = destSimTarget
				tracer.Target(flow, ttl)
			}
			ps = append(ps, p)
		}
		s.round(ps)
		tracer.EndRound(upTo, 30)

		s.wantDestination(dist)
		switch {
		case raisedAt == 0 && engine.State() == AlertDegraded:
			raisedAt = s.rounds
		case raisedAt != 0 && engine.State() != AlertDegraded:
			t.Fatalf("round %d: the loss alert raised in round %d was cleared; the destination still loses half its probes", s.rounds, raisedAt)
		}
		if t.Failed() {
			t.FailNow()
		}
	}
	// The target ignores ping: its loss counts once 20 echoes have gone
	// unanswered, and three bad rounds raise the alert.
	if raisedAt == 0 || raisedAt > 30 {
		t.Errorf("the loss alert was raised in round %d, want it raised by round 30", raisedAt)
	}
	if beyondProbed == 0 {
		t.Error("fixture: no round probed beyond the destination, so the test shows nothing")
	}
	pct, kind := s.sess().table.Get(dist).Loss()
	if kind != hop.LossMeasured || pct < 40 || pct > 60 {
		t.Errorf("destination row: loss %s, want about 50%% measured", hop.FormatLoss(pct, kind))
	}
	// The debug log's round lines count the hops of the path. The TTLs
	// probed beyond the destination are counted apart, so that a hidden TTL
	// that answered nothing is not reported as a silent hop.
	apart := 0
	for _, l := range s.log.containing(" hops probed") {
		if l == "" || strings.Contains(l, "round 1:") {
			continue
		}
		if !strings.Contains(l, fmt.Sprintf(" of %d hops probed", dist)) {
			t.Errorf("round line counts hops beyond the destination among those probed: %q", l)
		}
		// "1 TTL probed", "2 TTLs probed": the noun follows the count
		// (viaduct#319).
		if strings.Contains(l, " probed beyond the destination") {
			apart++
		}
	}
	if apart != beyondProbed {
		t.Errorf("%d round lines report TTLs probed beyond the destination, want %d", apart, beyondProbed)
	}
	one, many := 0, 0
	for _, l := range s.log.containing(" probed beyond the destination") {
		switch {
		case strings.HasSuffix(l, "; 1 TTL probed beyond the destination"):
			one++
		case strings.HasSuffix(l, " TTLs probed beyond the destination") && !strings.HasSuffix(l, "; 1 TTLs probed beyond the destination"):
			many++
		default:
			t.Errorf("round line: %q, want it to end \"1 TTL probed beyond the destination\" or \"N TTLs probed beyond the destination\"", l)
		}
	}
	if one == 0 || many == 0 {
		t.Errorf("fixture: %d lines with one TTL beyond the destination and %d with more, want some of each", one, many)
	}
	if moved := s.log.containing("destination moved"); len(moved) != 0 {
		t.Errorf("debug log: %q, want no move", moved)
	}
}

// The alert engine follows the destination's hop, whichever TTL that is.
// When the destination moves, the count of bad rounds and a raised alert
// carry over: the alert is about the destination, and it clears when the
// destination, at its new hop, has a good round.
func TestAlertEngine_KeepsItsStateWhenTheDestinationMoves(t *testing.T) {
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
	lostRound(e, 4) // the destination moved to a hop that is losing probes too
	if e.State() != AlertDegraded {
		t.Fatalf("state %v after 3 bad rounds at the destination, want degraded: a move does not start the count over", e.State())
	}
	lostRound(e, 5)
	if e.State() != AlertDegraded {
		t.Errorf("state %v after the destination moved back, want degraded", e.State())
	}
}
