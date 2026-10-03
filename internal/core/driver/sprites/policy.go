package sprites

import (
	"sort"
	"strings"

	sdk "github.com/superfly/sprites-go"
)

// GoToolchainHosts are the hosts GOTOOLCHAIN=auto needs at all times: toolchains
// and modules come from GOPROXY and are verified against sum.golang.org.
// https://go.dev/ref/mod#toolchains, https://go.dev/ref/mod#checksum-database
var GoToolchainHosts = []string{"proxy.golang.org", "sum.golang.org"}

// GoWarmHost serves toolchain zips: the proxy 302-redirects to
// storage.googleapis.com. A domain rule cannot restrict by bucket, so it is
// allowed only during the warm window (see warmGo).
const GoWarmHost = "storage.googleapis.com"

// BuildPolicy maps nexus egress hosts to a Sprites network policy. open => nil (no policy call).
func BuildPolicy(hosts []string, includeDefaults, open bool) *sdk.NetworkPolicy {
	if open {
		return nil
	}
	seen := make(map[string]struct{}, len(hosts))
	norm := make([]string, 0, len(hosts))
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if _, ok := seen[h]; ok {
			continue
		}
		seen[h] = struct{}{}
		norm = append(norm, h)
	}
	sort.Strings(norm)

	rules := make([]sdk.NetworkPolicyRule, 0, len(norm)+2)
	if includeDefaults {
		rules = append(rules, sdk.NetworkPolicyRule{Include: "defaults"})
	}
	for _, h := range norm {
		rules = append(rules, sdk.NetworkPolicyRule{Domain: h, Action: "allow"})
	}
	rules = append(rules, sdk.NetworkPolicyRule{Domain: "*", Action: "deny"})
	return &sdk.NetworkPolicy{Rules: rules}
}
