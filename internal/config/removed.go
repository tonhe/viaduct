package config

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	toml "github.com/pelletier/go-toml/v2"
)

// removedKey describes a config key that via no longer reads. A file that
// still carries one keeps loading: the key is ignored, the user is told once
// per run, and Save does not write it back.
type removedKey struct {
	name  string // name used in the warning
	since string // release that removed it
}

// removedKeys maps the lower-cased spelling of a key, as it can appear at the
// top level of the config file, to its description. ping_supplement is the
// spelling via wrote; PingSupplement is the name of the setting.
var removedKeys = map[string]removedKey{
	"ping_supplement": {name: "PingSupplement", since: "v0.3.0"},
	"pingsupplement":  {name: "PingSupplement", since: "v0.3.0"},
}

// warnOut receives config warnings. Tests replace it.
var warnOut io.Writer = os.Stderr

var (
	removedWarnedMu sync.Mutex
	removedWarned   = map[string]bool{}
)

// resetRemovedKeyWarnings forgets which removed keys were already reported.
func resetRemovedKeyWarnings() {
	removedWarnedMu.Lock()
	defer removedWarnedMu.Unlock()
	removedWarned = map[string]bool{}
}

// findRemovedKeys returns the names of the removed keys present at the top
// level of the TOML document, sorted and without duplicates. It returns nil
// when the document cannot be parsed.
func findRemovedKeys(data []byte) (names []string) {
	defer func() {
		if r := recover(); r != nil {
			names = nil
		}
	}()

	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	seen := map[string]bool{}
	for key := range raw {
		rk, ok := removedKeys[strings.ToLower(key)]
		if !ok || seen[rk.name] {
			continue
		}
		seen[rk.name] = true
		names = append(names, rk.name)
	}
	sort.Strings(names)
	return names
}

// warnRemovedKeys reports each removed key found in data on warnOut, once per
// run for each key.
func warnRemovedKeys(data []byte) {
	names := findRemovedKeys(data)
	if len(names) == 0 {
		return
	}

	since := map[string]string{}
	for _, rk := range removedKeys {
		since[rk.name] = rk.since
	}

	removedWarnedMu.Lock()
	defer removedWarnedMu.Unlock()
	for _, name := range names {
		if removedWarned[name] {
			continue
		}
		removedWarned[name] = true
		fmt.Fprintf(warnOut, "warning: config: %s was removed in %s and is ignored\n", name, since[name])
	}
}
