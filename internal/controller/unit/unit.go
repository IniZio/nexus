package unit

import (
	"bytes"
	"text/template"

	"github.com/IniZio/nexus/internal/core/vault"
)

// Params holds values substituted into the service unit templates.
type Params struct {
	NexusBinDir string
	ConfigPath  string
}

var systemdTmpl = template.Must(template.New("systemd").Parse(systemdTemplate))
var launchdTmpl = template.Must(template.New("launchd").Parse(launchdTemplate))

// RenderSystemd renders the systemd user unit for the nexus controller.
func RenderSystemd(p Params) ([]byte, error) {
	var buf bytes.Buffer
	if err := systemdTmpl.Execute(&buf, p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderLaunchd renders the launchd plist for the nexus controller.
func RenderLaunchd(p Params) ([]byte, error) {
	var buf bytes.Buffer
	if err := launchdTmpl.Execute(&buf, p); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CredentialName is the systemd LoadCredentialEncrypted name for the vault key.
// Matches vault.KeyCredentialName exactly.
const CredentialName = vault.KeyCredentialName
