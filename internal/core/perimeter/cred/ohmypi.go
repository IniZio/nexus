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

// Oh-my-pi (@oh-my-pi/pi-coding-agent, CLI binary omp as of 18.2.6) does not
// read OpenCode's auth.json and does not keep a single JSON bearer file.
//
// Live resolution order (packages/coding-agent + @oh-my-pi/pi-ai 18.2.6):
//  1. Optional auth-broker daemon when OMP_AUTH_BROKER_URL is set. The bearer
//     is OMP_AUTH_BROKER_TOKEN, else auth.broker.token in
//     <agentDir>/config.yml, else ~/.omp/auth-broker.token.
//  2. Otherwise a local SQLite vault at <agentDir>/agent.db, table
//     auth_credentials. Rows are per provider and are either
//     {type:api_key,key} or OAuth {access,refresh,expires}. Login is
//     `omp auth-broker login <provider>` or the in-TUI /login command.
//  3. Env-var fallback per provider (ANTHROPIC_OAUTH_TOKEN, ANTHROPIC_API_KEY,
//     OPENAI_API_KEY, …). `omp --api-key` overrides those for one run.
//
// A DedicatedCredStore is one access token. Picking a row out of the vault
// would guess a provider, so import refuses. CredentialFormatOhMyPiVault
// registers a nil ImportFn so CheckCred does not treat a missing vault as a
// broken credential and block sandbox create. SourceFn returns no token.

const (
	ohMyPiConfigDirName = ".omp"
	ohMyPiAgentDBName   = "agent.db"
	ohMyPiAppName       = "omp"
)

// sqliteMagic is the 16-byte header of every SQLite 3 database.
var sqliteMagic = []byte("SQLite format 3\x00")

// ErrOhMyPiVaultNotImportable is returned when the on-disk credential store
// exists and is a SQLite database. Nexus will not choose a provider row.
var ErrOhMyPiVaultNotImportable = errors.New("oh-my-pi credential store is a multi-provider SQLite vault; refusing to select a provider row")

func init() {
	// ImportFn is intentionally nil: CheckCred treats a nil ImportFn as OK,
	// so a missing vault does not block sandbox create. SourceFn returns no
	// token because the vault is not one bearer. ImportFromPathFn refuses
	// rather than sharing CredentialFormatNone's Claude importer.
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

// OhMyPiCredPath returns the absolute path of the SQLite vault omp reads.
//
// Resolution matches @oh-my-pi/pi-utils dirs.ts (18.2.6):
//   - A named OMP_PROFILE / PI_PROFILE derives its own agent directory and
//     ignores PI_CODING_AGENT_DIR. The override applies only to the default
//     profile, and not when it is exactly a bypassed PI_PROFILE's derived
//     agent directory. When it does apply, it disables the XDG redirect.
//   - Otherwise the agent directory is $HOME/<PI_CONFIG_DIR or .omp>/agent,
//     or $HOME/<config>/profiles/<profile>/agent when a profile is set
//     (PI_PROFILE is the fallback only when OMP_PROFILE is unset).
//   - On Linux and Darwin, if that default location is in use and
//     $XDG_DATA_HOME/omp (or .../profiles/<profile>) already exists, the vault
//     is $XDG_DATA_HOME/omp/agent.db. XDG is not used merely because the
//     variable is set.
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

// ImportOhMyPiCredentials locates the omp SQLite vault and refuses to import it.
//
// A missing file returns an error wrapping [os.ErrNotExist]. A present SQLite
// file returns an error wrapping [ErrOhMyPiVaultNotImportable] and is not
// modified. Anything else is an unreadable-store error. The function never
// returns a [DedicatedCredStore]: there is no single bearer to extract.
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
