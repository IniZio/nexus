package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/IniZio/nexus3/internal/core/config"
)

func init() {
	Register(Command{
		Name:    "config validate",
		Summary: "Load and validate .nexus/config.yaml; print the resolved path and an effective-config summary",
		Run:     runConfigValidate,
	})
}

const configValidateUsage = "config validate: usage: config validate [dir] [--json]"

type configValidateData struct {
	Path                 string                   `json:"path"`
	Version              int                      `json:"version"`
	Image                string                   `json:"image"`
	Containerfile        string                   `json:"containerfile"`
	ContainerfilePresent bool                     `json:"containerfile_present"`
	Egress               configValidateEgressData `json:"egress"`
}

type configValidateEgressData struct {
	Mode         string `json:"mode"`
	AllowHosts   int    `json:"allow_hosts"`
	PolicyHosts  int    `json:"policy_hosts"`
	SecretsHosts int    `json:"secrets_hosts"`
}

// runConfigValidate runs config.Load (the loader every other verb uses) and
// reports the outcome. Unlike other verbs an ABSENT file is a failure here:
// "no file found" must not read as "valid".
func runConfigValidate(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "config validate: " + err.Error()}
	}
	dir := "."
	switch fs.NArg() {
	case 0:
	case 1:
		dir = fs.Arg(0)
	default:
		return &UsageError{Msg: configValidateUsage}
	}

	cfg, cfgPath, err := config.Load(dir)
	if err != nil {
		return &CodedError{Code: ErrCodeInvalidArgument, Msg: err.Error(), Err: err}
	}
	if cfgPath == "" {
		absDir, absErr := filepath.Abs(dir)
		if absErr != nil {
			absDir = dir
		}
		return &CodedError{
			Code: ErrCodeInvalidArgument,
			Msg: fmt.Sprintf("config validate: no %s found from %s (searched up to the repository root)",
				config.ConfigRelPath, absDir),
		}
	}

	data, err := summarizeConfig(cfg, cfgPath)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "config validate: " + err.Error(), Err: err}
	}
	out.EmitSuccess("config_validate", data, renderConfigValidate(data))
	return nil
}

// config.Config carries no version field (Load only range-checks it), so the
// version is re-read leniently from the bytes Load already accepted.
func summarizeConfig(cfg config.Config, cfgPath string) (configValidateData, error) {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return configValidateData{}, err
	}
	var versionOnly struct {
		Version int `yaml:"version"`
	}
	if err := yaml.Unmarshal(raw, &versionOnly); err != nil {
		return configValidateData{}, fmt.Errorf("re-read version from %s: %w", cfgPath, err)
	}

	containerfile := filepath.Join(config.ProjectDir(cfgPath), ".nexus", "Containerfile")
	_, statErr := os.Stat(containerfile)

	secretHosts := make(map[string]struct{})
	for _, s := range cfg.Egress.Secrets {
		for _, h := range s.Hosts {
			secretHosts[strings.ToLower(h)] = struct{}{}
		}
	}
	eg := configValidateEgressData{
		AllowHosts:   len(cfg.Egress.Allow),
		PolicyHosts:  len(cfg.Egress.Policy),
		SecretsHosts: len(secretHosts),
	}
	switch {
	case eg.PolicyHosts > 0 || eg.SecretsHosts > 0:
		eg.Mode = "policy-gated"
	case eg.AllowHosts > 0:
		eg.Mode = "allow-only"
	default:
		eg.Mode = "default"
	}

	return configValidateData{
		Path:                 cfgPath,
		Version:              versionOnly.Version,
		Image:                cfg.Sandbox.Image,
		Containerfile:        containerfile,
		ContainerfilePresent: statErr == nil,
		Egress:               eg,
	}, nil
}

func renderConfigValidate(d configValidateData) string {
	image := d.Image
	if image == "" {
		image = "(unset)"
	}
	containerfile := d.Containerfile
	if !d.ContainerfilePresent {
		containerfile = "(absent)"
	}
	return fmt.Sprintf(
		"ok: %s\n  version:       %d\n  image:         %s\n  containerfile: %s\n  egress:        mode=%s allow=%d policy=%d secrets=%d",
		d.Path, d.Version, image, containerfile,
		d.Egress.Mode, d.Egress.AllowHosts, d.Egress.PolicyHosts, d.Egress.SecretsHosts,
	)
}
