package hop

import (
	"fmt"
	"sort"
	"strings"
)

// FormatFlowIDs formats a list of 0-indexed flow IDs into a compact display string.
func FormatFlowIDs(ids []int) string {
	if len(ids) == 0 {
		return "-"
	}
	sort.Ints(ids)
	// Check if consecutive for compact range display
	consecutive := true
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[i-1]+1 {
			consecutive = false
			break
		}
	}
	if consecutive && len(ids) > 2 {
		return fmt.Sprintf("%d-%d", ids[0]+1, ids[len(ids)-1]+1)
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id+1) // 1-indexed for display
	}
	return strings.Join(parts, ",")
}
