package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The nexus-subagent mod API is early access. These constants are the single
// source for the doctor check; claudeModVersion must match
// plugins/nexus-subagent/.claude-plugin/plugin.json (enforced by a test) and
// claudeModMinClaudeVersion is documented in plugins/nexus-subagent/README.md.
// The Claude Code plugin schema has no engines/compat field, so the range
// cannot live in the manifest.
const (
	claudeModPluginName       = "nexus-subagent"
	claudeModVersion          = "0.6.0"
	claudeModMinClaudeVersion = "2.1.288"
)

// claudeModState is what the probe observed about Claude Code and the mod.
type claudeModState struct {
	ClaudeVersion    string // empty when `claude` is not on PATH
	ModInstalled     bool
	ModVersion       string // may be empty if the registry records none
	RegistryReadable bool
}

var semverRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// cmpVersions compares dotted numeric versions; ok is false if either is
// unparseable.
func cmpVersions(a, b string) (int, bool) {
	pa, pb := semverRe.FindString(a), semverRe.FindString(b)
	if pa == "" || pb == "" {
		return 0, false
	}
	as, bs := strings.Split(pa, "."), strings.Split(pb, ".")
	for i := range as {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func probeClaudeMod() claudeModState {
	var st claudeModState
	if bin, err := exec.LookPath("claude"); err == nil {
		if out, err := exec.Command(bin, "--version").Output(); err == nil {
			st.ClaudeVersion = strings.TrimSpace(string(out))
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return st
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return st
	}
	var top struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(data, &top) != nil {
		return st
	}
	st.RegistryReadable = true
	for key, raw := range top.Plugins {
		if key != claudeModPluginName && !strings.HasPrefix(key, claudeModPluginName+"@") {
			continue
		}
		st.ModInstalled = true
		var one struct {
			Version string `json:"version"`
		}
		var many []struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(raw, &one) == nil {
			st.ModVersion = one.Version
		} else if json.Unmarshal(raw, &many) == nil && len(many) > 0 {
			st.ModVersion = many[0].Version
		}
	}
	return st
}

// checkClaudeMod is WARN-only (Optional): a missing Claude Code or mod never
// blocks sandbox creation.
func checkClaudeMod(probe func() claudeModState) CheckResult {
	cr := CheckResult{
		Name:        "claude_mod",
		Description: "nexus-subagent mod for Claude Code",
		Optional:    true,
	}
	st := probe()
	if st.ClaudeVersion == "" {
		cr.Detail = "Claude Code (`claude`) not found on PATH"
		cr.Remediation = "Install Claude Code to use nexus:worker subagents; ignore if you do not use it."
		return cr
	}
	var problems []string
	cr.Remediation = fmt.Sprintf("Install or update the mod: /plugin install %s (repo version %s).", claudeModPluginName, claudeModVersion)
	if c, ok := cmpVersions(st.ClaudeVersion, claudeModMinClaudeVersion); ok && c < 0 {
		problems = append(problems, fmt.Sprintf("claude %s is older than minimum %s supported by the mod", st.ClaudeVersion, claudeModMinClaudeVersion))
		cr.Remediation = "Upgrade Claude Code. " + cr.Remediation
	}
	switch {
	case !st.ModInstalled:
		problems = append(problems, "mod not installed")
	case st.ModVersion == "":
		problems = append(problems, "mod installed, version unknown")
	default:
		if c, ok := cmpVersions(st.ModVersion, claudeModVersion); ok && c != 0 {
			problems = append(problems, fmt.Sprintf("mod %s differs from repo version %s", st.ModVersion, claudeModVersion))
		}
	}
	if len(problems) > 0 {
		cr.Detail = strings.Join(problems, "; ")
		return cr
	}
	cr.OK = true
	cr.Detail = fmt.Sprintf("mod %s installed; claude %s (min %s)", st.ModVersion, st.ClaudeVersion, claudeModMinClaudeVersion)
	cr.Remediation = ""
	return cr
}
