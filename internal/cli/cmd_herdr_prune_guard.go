package cli

import (
	"fmt"
	"sort"
	"strings"
)

const herdrPruneGuardPrefix = "__herdr-plugin space-prune: --apply refused: "

// herdrGlobalPruneGuard returns a non-nil error when a global (non --workspace)
// prune --apply must be refused due to cross-session risk.
//
// Rules:
//   - allowForeign → always nil (caller opted in).
//   - cur != def → refuse: non-default herdr session is active.
//   - any binding with a foreign HerdrSession → refuse and list up to 5 distinct
//     foreign sessions with counts. Legacy bindings (HerdrSession == "") are safe.
func herdrGlobalPruneGuard(cur, def string, bindings []HerdrSpaceBinding, allowForeign bool) error {
	if allowForeign {
		return nil
	}

	// Non-default session selected via HERDR_SOCKET_PATH / HERDR_SESSION.
	if cur != def {
		return &UsageError{Msg: herdrPruneGuardPrefix +
			fmt.Sprintf("non-default herdr session selected (%s); "+
				"pass --allow-foreign-sessions or use --workspace to target a single sandbox", cur)}
	}

	// Collect foreign-session counts.
	counts := map[string]int{}
	for _, b := range bindings {
		if b.HerdrSession != "" && b.HerdrSession != cur {
			counts[b.HerdrSession]++
		}
	}
	if len(counts) == 0 {
		return nil
	}

	// Stable-sorted list of foreign sessions, capped at 5.
	sessions := make([]string, 0, len(counts))
	for s := range counts {
		sessions = append(sessions, s)
	}
	sort.Strings(sessions)

	var sb strings.Builder
	sb.WriteString(herdrPruneGuardPrefix)
	sb.WriteString("bindings owned by foreign herdr sessions would be pruned:\n")
	shown := sessions
	if len(shown) > 5 {
		shown = shown[:5]
	}
	for _, s := range shown {
		fmt.Fprintf(&sb, "  %s (%d binding(s))\n", s, counts[s])
	}
	if len(sessions) > 5 {
		fmt.Fprintf(&sb, "  … and %d more session(s)\n", len(sessions)-5)
	}
	sb.WriteString("pass --allow-foreign-sessions or use --workspace to target a single sandbox")
	return &UsageError{Msg: sb.String()}
}
