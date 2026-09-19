package cred

import (
	"errors"
	"fmt"
)

// Kiro CLI 2.22.1, from
// https://prod.download.cli.kiro.dev/stable/latest/manifest.json.
// The headless x86_64 tar.gz sha256 matches that manifest. The archive has
// one top-level directory (kirocli/, confirmed from the sibling zip's
// central directory: bin/kiro-cli, bin/kiro-cli-chat, bin/kiro-cli-term),
// which is what RecipeKindTarball's --strip-components=1 expects. There is
// no npm package and no sandbox-templates OCI image. The URL hardcodes
// x86_64 because RenderRecipeLayer substitutes {ARCH} with "x64"/"arm64",
// while Kiro publishes x86_64/aarch64. An arm64 build has no SHA256ByArch
// entry and the renderer refuses.
const (
	KiroCLIVersion = "2.22.1"

	// KiroTarballSHA256X64 is the sha256 of kirocli-x86_64-linux.tar.gz
	// for KiroCLIVersion, copied from the stable manifest.
	KiroTarballSHA256X64 = "02b060b989d011519c8eaacf460e28e918efd106df8c75ceb7f9c0aaed80f2fe"

	// KiroAPIKeyEnv is the headless credential (ksk_… keys; kiro.dev docs).
	// whoami reports accountType ApiKey for any non-empty value without a
	// network call. It is not AgentProfile.APIKeyEnvVar: see
	// ImportKiroCredentials.
	KiroAPIKeyEnv = "KIRO_API_KEY"
)

// ErrKiroNoBroker is returned by ImportKiroCredentials.
var ErrKiroNoBroker = errors.New("cred: kiro: no brokerable host credential")

// ImportKiroCredentials refuses. There is no JSON credential file to swap,
// and wiring KIRO_API_KEY would plant a placeholder the proxy cannot redeem.
//
// Measured on kiro-cli 2.22.1 (2026-09-19), not assumed:
//
// Login (`kiro-cli login`, including `--use-device-flow`) is browser or
// device-code OAuth: Builder ID, Google, GitHub, or Identity Center. After
// login the CLI stores access and refresh tokens in
// ~/.local/share/kiro-cli/data.sqlite3 (table auth_kv). This host's
// database exists and has no login row. The seeder writes a JSON file, not
// that sqlite database. The binary links the AWS SDK credential chain, but
// whoami with neither KIRO_API_KEY nor a sqlite token returns account null.
//
// KIRO_API_KEY is a real copyable bearer. A throwaway key, intercepted
// with a local MITM the CLI was pointed at (Q_CUSTOM_CERT), was sent as
// Authorization: Bearer plus Tokentype: API_KEY. GetProfile
// (X-Amz-Target AmazonCodeWhispererService.GetProfile) fans out across
// management.us-east-1.kiro.dev, management.eu-central-1.kiro.dev, and the
// gov/iso management hosts. The prompt itself is
// POST https://runtime.us-east-1.kiro.dev/
// AmazonCodeWhispererStreamingService.GenerateAssistantResponse, same
// bearer. q.us-east-1.amazonaws.com also receives it (SendTelemetryEvent);
// that failure is non-fatal. whoami does not contact a server.
//
// Nexus mints one env placeholder, registered only for CredentialedHost,
// and swaps Authorization scoped to that host. Kiro presents that same
// bearer to a second host. The inference call would leave with an
// unredeemable placeholder. CredentialedHostSuffix ".kiro.dev" unscoped-
// swaps only when egress is explicitly open, and would also cover download
// and telemetry hosts. This function therefore returns no token.
func ImportKiroCredentials(AgentProfile) (*DedicatedCredStore, error) {
	return nil, fmt.Errorf("%w", ErrKiroNoBroker)
}
