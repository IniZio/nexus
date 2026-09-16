package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func checkHerdrProcesses(ctx context.Context, lister func(context.Context) ([]HerdrProc, error)) CheckResult {
	cr := CheckResult{
		Name:        "herdr_processes",
		Description: "herdr-related process health (stale duplicates)",
	}

	procs, err := lister(ctx)
	if err != nil {
		cr.OK = false
		cr.Detail = fmt.Sprintf("ps failed: %v", err)
		return cr
	}

	serversBySess := map[string][]HerdrProc{}
	bridgesBySess := map[string][]HerdrProc{}
	var agents []HerdrProc

	for _, p := range procs {
		switch p.Kind {
		case HerdrServer:
			serversBySess[p.Session] = append(serversBySess[p.Session], p)
		case HerdrRemoteClientBridge:
			bridgesBySess[p.Session] = append(bridgesBySess[p.Session], p)
		case Nexus3ClientAgent:
			agents = append(agents, p)
		}
	}

	var remLines []string

	for sess, group := range serversBySess {
		if len(group) <= 1 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Start.Before(group[j].Start) })
		for _, p := range group[:len(group)-1] {
			remLines = append(remLines,
				fmt.Sprintf("stale herdr server pid %d (session %s): kill %d; herdr --session %s server stop", p.PID, sess, p.PID, sess))
		}
	}

	for sess, group := range bridgesBySess {
		if len(group) <= 1 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Start.Before(group[j].Start) })
		for _, p := range group[:len(group)-1] {
			remLines = append(remLines,
				fmt.Sprintf("stale herdr remote-client-bridge pid %d (session %s): kill %d", p.PID, sess, p.PID))
		}
	}

	if len(agents) > 1 {
		sort.Slice(agents, func(i, j int) bool { return agents[i].Start.Before(agents[j].Start) })
		for _, p := range agents[:len(agents)-1] {
			remLines = append(remLines,
				fmt.Sprintf("stale nexus3-client-agent pid %d: kill %d", p.PID, p.PID))
		}
	}

	if len(remLines) == 0 {
		cr.OK = true
		cr.Detail = "no stale herdr processes detected"
		return cr
	}

	sort.Strings(remLines)
	cr.OK = false
	cr.Detail = fmt.Sprintf("%d stale herdr process(es) detected", len(remLines))
	cr.Remediation = strings.Join(remLines, "\n")
	return cr
}
