package authrelay

import "testing"

func TestRelayEnv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		binding string
		value   string
		want    map[string]string
	}{
		{
			name:    "github",
			binding: "github",
			value:   "pat",
			want: map[string]string{
				"NEXUS_AUTH_BINDING": "github",
				"NEXUS_AUTH_VALUE":   "pat",
				"GITHUB_TOKEN":       "pat",
				"GH_TOKEN":           "pat",
			},
		},
		{
			name:    "github_mixed_case",
			binding: "GitHub",
			value:   "pat",
			want: map[string]string{
				"NEXUS_AUTH_BINDING": "GitHub",
				"NEXUS_AUTH_VALUE":   "pat",
				"GITHUB_TOKEN":       "pat",
				"GH_TOKEN":           "pat",
			},
		},
		{
			name:    "opencode",
			binding: "opencode",
			value:   "tok",
			want: map[string]string{
				"NEXUS_AUTH_BINDING": "opencode",
				"NEXUS_AUTH_VALUE":   "tok",
				"OPENCODE_API_KEY":   "tok",
			},
		},
		{
			name:    "codex_api_key",
			binding: "codex",
			value:   "sk-test",
			want: map[string]string{
				"NEXUS_AUTH_BINDING": "codex",
				"NEXUS_AUTH_VALUE":   "sk-test",
				"OPENAI_API_KEY":     "sk-test",
			},
		},
		{
			name:    "codex_non_api_value_no_openai_env",
			binding: "codex",
			value:   "oauth-via-host-config",
			want: map[string]string{
				"NEXUS_AUTH_BINDING": "codex",
				"NEXUS_AUTH_VALUE":   "oauth-via-host-config",
			},
		},
		{
			name:    "openai_binding",
			binding: "openai",
			value:   "sk-any",
			want: map[string]string{
				"NEXUS_AUTH_BINDING": "openai",
				"NEXUS_AUTH_VALUE":   "sk-any",
				"OPENAI_API_KEY":     "sk-any",
			},
		},
		{
			name:    "claude",
			binding: "claude",
			value:   "k",
			want: map[string]string{
				"NEXUS_AUTH_BINDING": "claude",
				"NEXUS_AUTH_VALUE":   "k",
				"ANTHROPIC_API_KEY":  "k",
			},
		},
		{
			name:    "openrouter",
			binding: "openrouter",
			value:   "or-key",
			want: map[string]string{
				"NEXUS_AUTH_BINDING": "openrouter",
				"NEXUS_AUTH_VALUE":   "or-key",
				"OPENROUTER_API_KEY": "or-key",
			},
		},
		{
			name:    "minimax",
			binding: "minimax",
			value:   "mm-key",
			want: map[string]string{
				"NEXUS_AUTH_BINDING": "minimax",
				"NEXUS_AUTH_VALUE":   "mm-key",
				"MINIMAX_API_KEY":    "mm-key",
			},
		},
		{
			name:    "custom",
			binding: "custom",
			value:   "x",
			want: map[string]string{
				"NEXUS_AUTH_BINDING": "custom",
				"NEXUS_AUTH_VALUE":   "x",
			},
		},
		{
			name:    "amp_binding_via_registry",
			binding: "amp",
			value:   "sk-ant-xxx",
			want: map[string]string{
				"NEXUS_AUTH_BINDING": "amp",
				"NEXUS_AUTH_VALUE":   "sk-ant-xxx",
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := RelayEnv(tc.binding, tc.value)
			if len(got) != len(tc.want) {
				t.Fatalf("len %d, want %d: got=%v want=%v", len(got), len(tc.want), got, tc.want)
			}
			for k, wv := range tc.want {
				if got[k] != wv {
					t.Fatalf("%q: got %q want %q", k, got[k], wv)
				}
			}
		})
	}
}
