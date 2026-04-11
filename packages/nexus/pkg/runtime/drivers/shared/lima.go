package shared

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
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

var limaShellNoiseFragments = []string{
	"mux_client_request_session: session request failed: Session open refused by peer",
	"ux_client_request_session: session request failed: Session open refused by peer",
	"ControlSocket ",
	"already exists, disconnecting",
	"disabling multiplexing",
	"Exiting ssh session for the instance",
}

func SanitizeLimaShellChunk(chunk string) string {
	trimmed := strings.TrimSpace(chunk)
	if trimmed == "" {
		return chunk
	}
	for _, noise := range limaShellNoiseFragments {
		if strings.Contains(trimmed, noise) {
			return ""
		}
	}
	return chunk
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

func EnsureLimaInstanceRunning(ctx context.Context, instance string, limactlOutput LimactlRun, limactlCombined LimactlRun) error {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		return fmt.Errorf("instance is required")
	}

	out, err := limactlOutput(ctx, "list", "--json", instance)
	if err != nil {
		if startOut, startErr := limactlCombined(ctx, "start", "--yes", "--name", instance, "template:default"); startErr != nil {
			return fmt.Errorf(
				"lima list failed for %s: %w; lima start failed for %s: %s",
				instance, err, instance, strings.TrimSpace(string(startOut)),
			)
		}
		return nil
	}
	trimmed := strings.TrimSpace(string(out))

	if trimmed == "" || trimmed == "[]" {
		if startOut, startErr := limactlCombined(ctx, "start", "--yes", "--name", instance, "template:default"); startErr != nil {
			return fmt.Errorf("lima start failed for %s: %s", instance, strings.TrimSpace(string(startOut)))
		}
		return nil
	}

	if strings.Contains(trimmed, `"status":"Running"`) {
		return nil
	}

	if strings.Contains(trimmed, `"status":"Stopped"`) {
		if startOut, startErr := limactlCombined(ctx, "start", "--yes", instance); startErr != nil {
			return fmt.Errorf("lima start failed for %s: %s", instance, strings.TrimSpace(string(startOut)))
		}
		return nil
	}

	return nil
}

func LimactlShellScript(ctx context.Context, instance, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "limactl", "shell", instance, "--", "sh", "-lc", script)
	return cmd.CombinedOutput()
}
