package cred

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

const (
	ohMyPiConfigDirName = ".omp"
	ohMyPiAgentDBName   = "agent.db"
	ohMyPiAppName       = "omp"
)

// sqliteMagic is the 16-byte header of every SQLite 3 database.
var sqliteMagic = []byte("SQLite format 3\x00")

// ErrOhMyPiVaultNotImportable is returned when the vault exists and is a SQLite database.
var ErrOhMyPiVaultNotImportable = errors.New("oh-my-pi credential store is a multi-provider SQLite vault; refusing to select a provider row")

func init() {
	agentRegistry[CredentialFormatOhMyPiVault] = AgentRegistration{
		SourceFn: func(AgentProfile) (CredentialSource, error) {
			return nil, nil
		},
		DefaultFromPathFn: func(AgentProfile) string {
			p, err := OhMyPiCredPath()
			if err != nil {
				return ""
			}
			return p
		},
		ImportFromPathFn: func(path string) (*DedicatedCredStore, error) {
			return importOhMyPiCredentialsAt(AgentProfile{Name: OhMyPiProfileName}, path)
		},
	}
}

var ohMyPiProfileNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// OhMyPiCredPath returns the absolute path of the SQLite vault omp reads; see doc/specs/cred/README.md.
func OhMyPiCredPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cred: OhMyPiCredPath: cannot determine home directory: %w", err)
	}

	configName := os.Getenv("PI_CONFIG_DIR")
	if configName == "" {
		configName = ohMyPiConfigDirName
	}
	baseRoot := filepath.Join(home, configName)

	profile, err := ohMyPiProfileFromEnv()
	if err != nil {
		return "", err
	}
	configRoot := baseRoot
	if profile != "" {
		configRoot = filepath.Join(baseRoot, "profiles", profile)
	}
	defaultAgent := filepath.Join(configRoot, "agent")

	agentDir := defaultAgent
	// dirs.ts resolveActiveAgentDirOverride: a named profile has no override.
	if profile == "" {
		if override := os.Getenv("PI_CODING_AGENT_DIR"); override != "" && !ohMyPiOverrideIsBypassedProfileDir(home, configName, override) {
			abs, err := filepath.Abs(override)
			if err != nil {
				return "", fmt.Errorf("cred: OhMyPiCredPath: PI_CODING_AGENT_DIR: %w", err)
			}
			agentDir = abs
		}
	}

	if agentDir == defaultAgent && ohMyPiXDGApplies() {
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			appRoot := filepath.Join(xdg, ohMyPiAppName)
			if profile != "" {
				appRoot = filepath.Join(appRoot, "profiles", profile)
			}
			if st, err := os.Stat(appRoot); err == nil && st.IsDir() {
				return filepath.Join(appRoot, ohMyPiAgentDBName), nil
			}
		}
	}
	return filepath.Join(agentDir, ohMyPiAgentDBName), nil
}

func ohMyPiXDGApplies() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin"
}

// ohMyPiOverrideIsBypassedProfileDir reports whether override is exactly the
// agent directory of a PI_PROFILE that an explicitly-set OMP_PROFILE bypassed.
// An invalid PI_PROFILE is ignored, matching dirs.ts readPiProfileFromEnvSafe.
// The comparison is against the raw env value, not a cleaned absolute path.
func ohMyPiOverrideIsBypassedProfileDir(home, configName, override string) bool {
	if _, ok := os.LookupEnv("OMP_PROFILE"); !ok {
		return false
	}
	name, err := normalizeOhMyPiProfile(os.Getenv("PI_PROFILE"))
	if err != nil || name == "" {
		return false
	}
	derived := filepath.Join(home, configName, "profiles", name, "agent")
	return override == derived
}

func ohMyPiProfileFromEnv() (string, error) {
	raw, ok := os.LookupEnv("OMP_PROFILE")
	if !ok {
		raw = os.Getenv("PI_PROFILE")
	}
	return normalizeOhMyPiProfile(raw)
}

func normalizeOhMyPiProfile(profile string) (string, error) {
	if profile == "" || profile == "default" {
		return "", nil
	}
	if profile == "." || profile == ".." || !ohMyPiProfileNameRe.MatchString(profile) {
		return "", fmt.Errorf("cred: OhMyPiCredPath: invalid OMP profile %q", profile)
	}
	return profile, nil
}

// ImportOhMyPiCredentials locates the omp SQLite vault and always refuses to import it; see doc/specs/cred/README.md.
func ImportOhMyPiCredentials(profile AgentProfile) (*DedicatedCredStore, error) {
	path, err := OhMyPiCredPath()
	if err != nil {
		return nil, err
	}
	return importOhMyPiCredentialsAt(profile, path)
}

// importOhMyPiCredentialsAt is the path-explicit entry point used by tests.
func importOhMyPiCredentialsAt(profile AgentProfile, path string) (*DedicatedCredStore, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cred: ImportOhMyPiCredentials %s: %w", path, os.ErrNotExist)
		}
		return nil, fmt.Errorf("cred: ImportOhMyPiCredentials %s: reading file: %w", path, err)
	}
	defer f.Close()

	hdr := make([]byte, len(sqliteMagic))
	n, err := io.ReadFull(f, hdr)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("cred: ImportOhMyPiCredentials %s: reading file: %w", path, err)
	}
	if n == len(sqliteMagic) && bytes.Equal(hdr[:n], sqliteMagic) {
		return nil, fmt.Errorf("cred: ImportOhMyPiCredentials %s (%s): %w", path, profile.Name, ErrOhMyPiVaultNotImportable)
	}
	return nil, fmt.Errorf("cred: ImportOhMyPiCredentials %s: file is not a SQLite vault", path)
}
