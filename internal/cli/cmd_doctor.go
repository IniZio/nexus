package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

func init() {
	Register(Command{
		Name:    "doctor",
		Summary: "Report substrate availability and capability check results",
		Run:     runDoctor,
	})
}

// ── JSON data types ───────────────────────────────────────────────────────────

type doctorCheckJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	OK          bool   `json:"ok"`
	Detail      string `json:"detail"`
	Remediation string `json:"remediation,omitempty"`
}

type doctorDataJSON struct {
	Substrate     string            `json:"substrate"`
	Selected      bool              `json:"selected"`
	OverrideValue string            `json:"override_value,omitempty"`
	OverrideMsg   string            `json:"override_message,omitempty"`
	Checks        []doctorCheckJSON `json:"checks"`
}

// ── runDoctor ─────────────────────────────────────────────────────────────────

// runDoctor is the implementation of the `nexus3 doctor` subcommand.
//
// Doctor always exits 0 — reporting "here is what is broken" is success.
// It never calls EmitError; it always calls EmitSuccess (possibly with
// selected=false). Emitting an error envelope here would produce the
// double-envelope problem described in cmd_recover.go.
//
// Doctor does not invoke any driver methods (no Observe, Start, or Stop).
// It reports the result of capability probes only.
func runDoctor(_ context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: err.Error()}
	}

	envVal := os.Getenv("NEXUS3_SUBSTRATE")

	// Handle overrides that bypass capability checks.
	switch envVal {
	case "none":
		data := doctorDataJSON{
			Substrate:     "none",
			Selected:      false,
			OverrideValue: "none",
			OverrideMsg:   "substrate disabled by NEXUS3_SUBSTRATE=none",
			Checks:        []doctorCheckJSON{},
		}
		out.EmitSuccess("doctor", data,
			"substrate: none (disabled by NEXUS3_SUBSTRATE=none)\nNo substrate selected.")
		return nil

	case "", "cloudhypervisor":
		// proceed to capability detection below

	default:
		data := doctorDataJSON{
			Substrate:     "none",
			Selected:      false,
			OverrideValue: envVal,
			OverrideMsg: fmt.Sprintf(
				"NEXUS3_SUBSTRATE=%q is not a recognised value; accepted values: cloudhypervisor, none",
				envVal,
			),
			Checks: []doctorCheckJSON{},
		}
		msg := fmt.Sprintf("error: NEXUS3_SUBSTRATE=%q is not a recognised value\n"+
			"accepted values: cloudhypervisor, none\n"+
			"tip: \"fake\" is not accepted outside Go tests — inject it directly in Go test code.", envVal)
		out.EmitSuccess("doctor", data, msg)
		return nil
	}

	// Run all capability checks. Doctor always runs every check and reports
	// all results, even after the first failure.
	rawChecks, drv := runAllChecks(defaultProbes())
	checks := toDoctorChecksJSON(rawChecks)

	substrate := "none"
	selected := drv != nil
	if selected {
		substrate = drv.Name()
	}

	data := doctorDataJSON{
		Substrate:     substrate,
		Selected:      selected,
		OverrideValue: envVal,
		Checks:        checks,
	}

	// Human-readable output.
	msg := formatDoctorHuman(substrate, selected, rawChecks)
	out.EmitSuccess("doctor", data, msg)
	return nil
}

// toDoctorChecksJSON converts the raw CheckResult slice from runAllChecks into
// the JSON-serialisable form. Extracted so tests can verify failed-check
// rendering (remediation text, ok:false) without invoking real probes.
func toDoctorChecksJSON(checks []CheckResult) []doctorCheckJSON {
	out := make([]doctorCheckJSON, 0, len(checks))
	for _, c := range checks {
		out = append(out, doctorCheckJSON{
			Name:        c.Name,
			Description: c.Description,
			OK:          c.OK,
			Detail:      c.Detail,
			Remediation: c.Remediation,
		})
	}
	return out
}

// formatDoctorHuman builds a readable multi-line report for human (non-JSON) mode.
func formatDoctorHuman(substrate string, selected bool, checks []CheckResult) string {
	var sb strings.Builder
	if selected {
		fmt.Fprintf(&sb, "substrate: %s [selected]\n", substrate)
	} else {
		fmt.Fprintf(&sb, "substrate: none [not selected]\n")
	}
	sb.WriteString("\nCapability checks:\n")
	for _, c := range checks {
		status := "OK  "
		if !c.OK {
			status = "FAIL"
		}
		fmt.Fprintf(&sb, "  [%s] %-16s %s\n", status, c.Name, c.Detail)
		if !c.OK && c.Remediation != "" {
			fmt.Fprintf(&sb, "         remediation: %s\n", c.Remediation)
		}
	}
	if selected {
		fmt.Fprintf(&sb, "\nSubstrate selected: %s", substrate)
	} else {
		sb.WriteString("\nNo substrate selected.")
		// Print the first failing check's remediation prominently.
		for _, c := range checks {
			if !c.OK && c.Remediation != "" {
				fmt.Fprintf(&sb, "\n\nTo fix: %s", c.Remediation)
				break
			}
		}
	}
	return sb.String()
}
