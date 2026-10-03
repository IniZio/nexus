package sprites

import (
	"encoding/json"
	"testing"
)

func TestPolicyBuild(t *testing.T) {
	cases := []struct {
		name     string
		hosts    []string
		defaults bool
		open     bool
		want     string
	}{
		{"defaults+hosts", []string{"github.com", "*.npmjs.org"}, true, false,
			`{"rules":[{"include":"defaults"},{"domain":"*.npmjs.org","action":"allow"},{"domain":"github.com","action":"allow"},{"domain":"*","action":"deny"}]}`},
		{"no defaults", []string{"github.com"}, false, false,
			`{"rules":[{"domain":"github.com","action":"allow"},{"domain":"*","action":"deny"}]}`},
		{"closed empty", nil, false, false,
			`{"rules":[{"domain":"*","action":"deny"}]}`},
		{"normalize", []string{" GitHub.com ", "github.com", "", "  ", "*.GitHub.com", "api.github.com"}, true, false,
			`{"rules":[{"include":"defaults"},{"domain":"*.github.com","action":"allow"},{"domain":"api.github.com","action":"allow"},{"domain":"github.com","action":"allow"},{"domain":"*","action":"deny"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := BuildPolicy(c.hosts, c.defaults, c.open)
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != c.want {
				t.Fatalf("got %s\nwant %s", b, c.want)
			}
		})
	}
}

func TestPolicyOpenNil(t *testing.T) {
	if p := BuildPolicy([]string{"github.com"}, true, true); p != nil {
		t.Fatalf("open must return nil, got %+v", p)
	}
}

func TestPolicyGoHostsDefaultDeny(t *testing.T) {
	p := BuildPolicy(append([]string{"github.com"}, GoToolchainHosts...), false, false)
	b, _ := json.Marshal(p)
	want := `{"rules":[{"domain":"github.com","action":"allow"},{"domain":"proxy.golang.org","action":"allow"},{"domain":"sum.golang.org","action":"allow"},{"domain":"*","action":"deny"}]}`
	if string(b) != want {
		t.Fatalf("got %s\nwant %s", b, want)
	}
}

func TestWithGoToolchain(t *testing.T) {
	if got := withGoToolchain(nil)["GOTOOLCHAIN"]; got != "auto" {
		t.Fatalf("got %q", got)
	}
	got := withGoToolchain(map[string]string{"GOTOOLCHAIN": "local", "A": "b"})
	if got["GOTOOLCHAIN"] != "local" || got["A"] != "b" {
		t.Fatalf("override lost: %v", got)
	}
}
