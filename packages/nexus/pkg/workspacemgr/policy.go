package workspacemgr

import "fmt"

type GitCredentialMode string

const (
	GitCredentialHostHelper      GitCredentialMode = "host-helper"
	GitCredentialEphemeralHelper GitCredentialMode = "ephemeral-helper"
	GitCredentialNone            GitCredentialMode = "none"
)

type AuthProfile string

const (
	AuthProfileGitCfg AuthProfile = "gitconfig"
)

type Policy struct {
	AuthProfiles      []AuthProfile     `json:"authProfiles,omitempty"`
	SSHAgentForward   bool              `json:"sshAgentForward,omitempty"`
	GitCredentialMode GitCredentialMode `json:"gitCredentialMode,omitempty"`
}

func ValidatePolicy(p Policy) error {
	if p.GitCredentialMode != "" {
		switch p.GitCredentialMode {
		case GitCredentialHostHelper, GitCredentialEphemeralHelper, GitCredentialNone:
		default:
			return fmt.Errorf("invalid gitCredentialMode: %s", p.GitCredentialMode)
		}
	}

	for _, profile := range p.AuthProfiles {
		switch profile {
		case AuthProfileGitCfg:
		default:
			return fmt.Errorf("invalid auth profile: %s", profile)
		}
	}

	return nil
}
