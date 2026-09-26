package vault

import (
	"fmt"
	"os/user"
)

// PrincipalEnv is the environment variable name used to pass the principal
// from the controller into a sandbox.
const PrincipalEnv = "NEXUS_PRINCIPAL"

// KeyCredentialName is the systemd credential name for the vault encryption key.
const KeyCredentialName = "nexus-vault-key"

// LocalPrincipal returns the principal for the current OS user in the form
// "local:<username>".
func LocalPrincipal() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("vault: LocalPrincipal: %w", err)
	}
	return "local:" + u.Username, nil
}

// SlackPrincipal returns the principal for a Slack user in the form
// "slack:<team>:<user>".
func SlackPrincipal(team, userID string) string {
	return "slack:" + team + ":" + userID
}
