package shared

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"
	"time"
)

func ListLimaInstances(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "limactl", "ls", "--format", "{{.Name}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(out), "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names, nil
}

func FilterCandidatesStrict(candidates, discovered []string) []string {
	if len(candidates) == 0 || len(discovered) == 0 {
		return candidates
	}

	availableSet := make(map[string]struct{}, len(discovered))
	for _, name := range discovered {
		availableSet[strings.TrimSpace(name)] = struct{}{}
	}

	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := availableSet[candidate]; ok {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) > 0 {
		return filtered
	}
	return candidates
}

func FilterCandidatesSortedFallback(candidates, discovered []string) []string {
	if len(candidates) == 0 || len(discovered) == 0 {
		return candidates
	}

	availableSet := make(map[string]struct{}, len(discovered))
	for _, name := range discovered {
		availableSet[strings.TrimSpace(name)] = struct{}{}
	}

	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, ok := availableSet[candidate]; ok {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) > 0 {
		return filtered
	}

	fallback := make([]string, 0, len(availableSet))
	for name := range availableSet {
		if name != "" {
			fallback = append(fallback, name)
		}
	}
	sort.Strings(fallback)
	return fallback
}

func InstanceCandidates(instanceName string, base []string) []string {
	trimmed := strings.TrimSpace(instanceName)
	if trimmed == "" {
		out := make([]string, len(base))
		copy(out, base)
		return out
	}
	out := make([]string, 0, len(base)+1)
	out = append(out, trimmed)
	for _, candidate := range base {
		if candidate == trimmed {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func IsTransientLimaShellError(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	for _, marker := range []string{
		"kex_exchange_identification",
		"connection reset by peer",
		"connection closed by remote host",
		"broken pipe",
		"mux_client_request_session",
		"session open refused by peer",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

type LimactlRun func(ctx context.Context, args ...string) ([]byte, error)

func DefaultLimactlOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "limactl", args...)
	return cmd.Output()
}

func DefaultLimactlCombinedOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "limactl", args...)
	return cmd.CombinedOutput()
}

// limaInstanceInfo holds the fields we care about from `limactl list --json`.
type limaInstanceInfo struct {
	Status       string `json:"status"`
	SSHLocalPort int    `json:"sshLocalPort"`
}

// parseLimaInstanceInfo extracts status and SSH port from `limactl list --json` output.
// It handles both array and single-object responses.
func parseLimaInstanceInfo(data string) (limaInstanceInfo, bool) {
	data = strings.TrimSpace(data)
	if data == "" || data == "[]" {
		return limaInstanceInfo{}, false
	}
	var arr []limaInstanceInfo
	if err := json.Unmarshal([]byte(data), &arr); err == nil && len(arr) > 0 {
		return arr[0], true
	}
	var single limaInstanceInfo
	if err := json.Unmarshal([]byte(data), &single); err == nil {
		return single, true
	}
	return limaInstanceInfo{}, false
}

// probeLimaSSH returns true when the local SSH port is accepting TCP connections.
func probeLimaSSH(port int, timeout time.Duration) bool {
	if port <= 0 {
		return false
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func EnsureLimaInstanceRunning(ctx context.Context, instance string, limactlOutput LimactlRun, limactlCombined LimactlRun) error {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		return fmt.Errorf("instance is required")
	}

	out, err := limactlOutput(ctx, "list", "--json", instance)
	if err != nil {
		return fmt.Errorf("lima list failed for %s: %w", instance, err)
	}
	trimmed := strings.TrimSpace(string(out))

	if trimmed == "" || trimmed == "[]" {
		return fmt.Errorf("lima instance %s is missing; run `nexus init --force` to create it with Nexus runtime settings", instance)
	}

	info, ok := parseLimaInstanceInfo(trimmed)
	if !ok {
		// Fallback: if we can't parse, just try to start.
		_, _ = limactlCombined(ctx, "start", "--yes", instance)
		return nil
	}

	status := strings.ToLower(strings.TrimSpace(info.Status))

	if status == "running" {
		// Verify SSH is actually reachable to catch zombie instances.
		if probeLimaSSH(info.SSHLocalPort, 3*time.Second) {
			return nil
		}
		// Zombie: status says Running but SSH is dead. Force-cycle the instance.
		_, _ = limactlCombined(ctx, "stop", "--force", instance)
	}

	if startOut, startErr := limactlCombined(ctx, "start", "--yes", instance); startErr != nil {
		return fmt.Errorf("lima start failed for %s: %s", instance, strings.TrimSpace(string(startOut)))
	}
	return nil
}
