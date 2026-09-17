package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/semver"
)

const herdrUpdateRemedy = "run: herdr plugin install IniZio/nexus/plugins/herdr"

type versionCheckOutcome int

const (
	vcoSame           versionCheckOutcome = 0
	vcoPinNewer       versionCheckOutcome = 10
	vcoInstalledNewer versionCheckOutcome = 11
	vcoDev            versionCheckOutcome = 12
	vcoUnparsable     versionCheckOutcome = 1
)

type versionCheckResult struct {
	Outcome   versionCheckOutcome
	Installed string
	Pin       string
	Message   string
}

// herdrCheckVersionSkew compares installed against pin. Pure; no I/O.
// MUTATION PROOF: "-dev" check is explicit; removing it makes TestHerdrVersionCheck_DevMutationPinned fail.
func herdrCheckVersionSkew(installed, pin string) versionCheckResult {
	installed = strings.TrimSpace(installed)
	pin = strings.TrimSpace(pin)

	if strings.Contains(installed, "-dev") {
		return versionCheckResult{
			Outcome:   vcoDev,
			Installed: installed,
			Pin:       pin,
			Message:   fmt.Sprintf("dev build %q — kept (set NEXUS_FORCE_DOWNLOAD=1 to override)", installed),
		}
	}

	instSV := installed
	if !strings.HasPrefix(instSV, "v") {
		instSV = "v" + instSV
	}
	pinSV := pin
	if !strings.HasPrefix(pinSV, "v") {
		pinSV = "v" + pinSV
	}

	if !semver.IsValid(instSV) || !semver.IsValid(pinSV) {
		return versionCheckResult{
			Outcome:   vcoUnparsable,
			Installed: installed,
			Pin:       pin,
			Message:   fmt.Sprintf("version unparsable: installed=%q pin=%q — cannot compare", installed, pin),
		}
	}

	cmp := semver.Compare(instSV, pinSV)
	switch {
	case cmp == 0:
		return versionCheckResult{
			Outcome:   vcoSame,
			Installed: installed,
			Pin:       pin,
			Message:   fmt.Sprintf("version ok: %s (matches pin)", installed),
		}
	case cmp > 0:
		return versionCheckResult{
			Outcome:   vcoInstalledNewer,
			Installed: installed,
			Pin:       pin,
			Message:   fmt.Sprintf("installed %s > pinned %s — kept (set NEXUS_FORCE_DOWNLOAD=1 to override)", installed, pin),
		}
	default:
		return versionCheckResult{
			Outcome:   vcoPinNewer,
			Installed: installed,
			Pin:       pin,
			Message:   fmt.Sprintf("version skew: installed %s < pinned %s\n%s", installed, pin, herdrUpdateRemedy),
		}
	}
}

// herdrVersionCheck implements nexus herdr version-check (exit 0=same 1=unparsable 10=pin-newer 11=inst-newer 12=dev).
func herdrVersionCheck(ctx context.Context, args []string, w io.Writer) error {
	fs := flag.NewFlagSet("herdr version-check", flag.ContinueOnError)
	pinFile := fs.String("pin", "", "path to nexus-version pin file")
	abiFile := fs.String("abi", "", "path to abi file")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: err.Error()}
	}

	pluginRoot := os.Getenv("HERDR_PLUGIN_ROOT")
	resolvedPin := *pinFile
	if resolvedPin == "" {
		if pluginRoot == "" {
			fmt.Fprintln(w, "version-check: HERDR_PLUGIN_ROOT unset; cannot locate nexus-version")
			return nil
		}
		resolvedPin = filepath.Join(pluginRoot, "nexus-version")
	}

	pinBytes, err := os.ReadFile(resolvedPin)
	if err != nil {
		fmt.Fprintf(w, "version-check: cannot read pin file %s: %v\n", resolvedPin, err)
		return nil
	}

	result := herdrCheckVersionSkew(version, string(pinBytes))

	if result.Outcome == vcoPinNewer {
		fmt.Fprintln(os.Stderr, result.Message)
	} else {
		fmt.Fprintln(w, result.Message)
	}

	resolvedABI := *abiFile
	if resolvedABI == "" && pluginRoot != "" {
		resolvedABI = filepath.Join(pluginRoot, "abi")
	}
	if resolvedABI != "" {
		if abiBytes, abiErr := os.ReadFile(resolvedABI); abiErr == nil {
			if fileABI := strings.TrimSpace(string(abiBytes)); fileABI != herdrPluginABIVersion {
				fmt.Fprintf(os.Stderr, "ABI skew: file has %q, binary expects %q\n%s\n", fileABI, herdrPluginABIVersion, herdrUpdateRemedy)
			}
		}
	}

	if result.Outcome == vcoSame {
		return nil
	}
	return &ExitCodeError{Code: int32(result.Outcome)}
}
