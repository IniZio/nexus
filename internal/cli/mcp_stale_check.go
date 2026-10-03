package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// listStaleMCPProcs returns PIDs of `nexus mcp` processes whose executable was
// replaced on disk (/proc/<pid>/exe resolves to "... (deleted)"). Linux only.
func listStaleMCPProcs() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		exe, err := os.Readlink(filepath.Join("/proc", e.Name(), "exe"))
		if err != nil || !strings.HasSuffix(exe, " (deleted)") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := bytes.Split(bytes.TrimRight(raw, "\x00"), []byte{0})
		if len(args) >= 2 && filepath.Base(string(args[0])) == "nexus" && string(args[1]) == "mcp" {
			pids = append(pids, pid)
		}
	}
	return pids
}

func checkStaleMCPProcs(lister func() []int) CheckResult {
	cr := CheckResult{
		Name:        "mcp_stale",
		Description: "long-lived `nexus mcp` servers running the current binary",
		Optional:    true,
		OK:          true,
		Detail:      "none stale",
	}
	pids := lister()
	if len(pids) == 0 {
		return cr
	}
	cr.OK = false
	cr.Detail = fmt.Sprintf("%d stale `nexus mcp` process(es) running a replaced binary: pids %v", len(pids), pids)
	cr.Remediation = "Restart the agent session or reconnect the nexus MCP server (e.g. /mcp in Claude Code)."
	return cr
}
