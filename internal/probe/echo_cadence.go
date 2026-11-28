package probe

// defaultEchoBudget is the per-round echo budget sendEchoes uses when
// Config.EchoBudget is zero or negative.
const defaultEchoBudget = 64

// EchoRounds returns how many rounds the echo tier takes to ping every one of
// targets once, given a per-round budget (Config.EchoBudget; zero or less
// means the default of 64). It counts targets the way SubmitEchoTargets keeps
// them, at most maxEchoTargetsPerTTL per TTL, and sendEchoes round-robins
// through those budget at a time. It returns 1 when everything fits in one
// round, and 1 for no targets.
func EchoRounds(targets []EchoTarget, budget int) int {
	if budget <= 0 {
		budget = defaultEchoBudget
	}
	kept := 0
	perTTL := make(map[int]int)
	for _, tgt := range targets {
		if perTTL[tgt.TTL] >= maxEchoTargetsPerTTL {
			continue
		}
		perTTL[tgt.TTL]++
		kept++
	}
	if kept <= budget {
		return 1
	}
	return (kept + budget - 1) / budget
}
