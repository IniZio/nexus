package cred

import (
	"errors"
	"fmt"
)

// Codex OAuth constants, copied from openai/codex
// (codex-rs/login/src/auth/manager.rs, main, 2026-09-19). Not wired into
// [NewRefresher]: the CLI posts the refresh grant as JSON
// (TokenEncoding::Json in codex-rs/login/src/oauth/client.rs), and
// golang.org/x/oauth2 posts application/x-www-form-urlencoded.
const (
	// CodexClientID is the public PKCE client. There is no client_secret.
	// Override in the CLI is CODEX_APP_SERVER_LOGIN_CLIENT_ID.
	CodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// CodexTokenEndpoint is the refresh endpoint. Override in the CLI is
	// CODEX_REFRESH_TOKEN_URL_OVERRIDE.
	CodexTokenEndpoint = "https://auth.openai.com/oauth/token"
)

// ErrCodexNoBroker is returned by ImportCodexCredentials.
var ErrCodexNoBroker = errors.New("cred: codex: no brokerable host credential")

// ImportCodexCredentials refuses. Wiring any of Codex's bearers would plant
// a placeholder the one-host proxy cannot redeem.
//
// Measured from openai/codex main (2026-09-19), not assumed:
//
// ChatGPT login stores an access+refresh pair in $CODEX_HOME/auth.json
// (default ~/.codex/auth.json): auth_mode, OPENAI_API_KEY, tokens
// {id_token, access_token, refresh_token, account_id}, last_refresh.
// Inference for AuthMode::Chatgpt uses
// https://chatgpt.com/backend-api/codex (model-provider-info CHATGPT_CODEX_BASE_URL).
// Refresh posts JSON to CodexTokenEndpoint with CodexClientID. The access
// token is a JWT (parse_chatgpt_jwt_claims); a hex placeholder fails that
// parse and the CLI refreshes. The seeder's credential file is a flat
// JSON object, not this nested tokens object, so it cannot stand in for
// auth.json. Giving the guest the refresh token would send a second secret
// to auth.openai.com.
//
// CODEX_ACCESS_TOKEN is not that OAuth access token. classify_codex_access_token
// treats an "at-" prefix as a personal access token and everything else,
// including a 64-hex placeholder, as an agent-identity JWT.
// PersonalAccessTokenAuth::load sends the bearer to
// https://auth.openai.com/api/accounts/v1/user-auth-credential/whoami, then
// inference sends the same bearer to chatgpt.com. Agent-identity registration
// is https://auth.openai.com/api/accounts/v1/agent/register, then the same
// bearer goes to chatgpt.com. JWT-shaping the placeholder would not collapse
// those hosts into one.
//
// CODEX_API_KEY (and the OPENAI_API_KEY field written by
// `codex login --with-api-key`) is a different bearer, sent to
// https://api.openai.com/v1. A placeholder registered only for chatgpt.com
// would be forwarded to api.openai.com unchanged.
//
// Nexus mints one env placeholder, emitted for CredentialedHost, and
// ResolveScoped swaps it only for hosts that placeholder was registered
// for. RegisterPlaceholderForHost could extend one placeholder, but the
// seeder does not call it, and teaching it to is outside this file scope.
// CredentialedHostSuffix unscoped-swaps only when egress is explicitly open.
func ImportCodexCredentials(AgentProfile) (*DedicatedCredStore, error) {
	return nil, fmt.Errorf("%w", ErrCodexNoBroker)
}
