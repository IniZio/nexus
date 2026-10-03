//go:build linux

package supervisor

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
)

const supervisorOOMScoreAdj = 800

const procOOMScoreAdj = "/proc/self/oom_score_adj"

var writeOOMScoreAdj = func(path string, val int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(val)), 0o644)
}

var readOOMScoreAdj = func(path string) ([]byte, error) { return os.ReadFile(path) }

// raiseOOMScoreAdj makes the supervisor and every child it spawns (inherited
// across fork/exec) preferred OOM victims. Never lowers; never fails boot.
func raiseOOMScoreAdj() {
	if b, err := readOOMScoreAdj(procOOMScoreAdj); err == nil {
		if cur, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil && cur >= supervisorOOMScoreAdj {
			return
		}
	}
	if err := writeOOMScoreAdj(procOOMScoreAdj, supervisorOOMScoreAdj); err != nil {
		slog.Warn("supervisor.oom_score_adj_failed", "target", supervisorOOMScoreAdj, "err", err)
	}
}
