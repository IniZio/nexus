package cli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/config"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/vault"
)

// fakeVault implements vault.Vault for testing herdrGitHubLinked.
// If records is non-nil, Source enforces AllowedProjects per Record.AllowsProject.
// Otherwise, sourceErr is returned directly (backward-compat for single-error cases).
type fakeVault struct {
	sourceErr       error
	records         map[vault.Key]vault.Record
	capturedKey     vault.Key
	capturedProject string
}

func (f *fakeVault) Get(_ context.Context, key vault.Key) (vault.Record, error) {
	return vault.Record{}, errors.New("not implemented")
}
func (f *fakeVault) Put(_ context.Context, key vault.Key, record vault.Record) error {
	return errors.New("not implemented")
}
func (f *fakeVault) Delete(_ context.Context, key vault.Key) error {
	return errors.New("not implemented")
}
func (f *fakeVault) List(_ context.Context) ([]vault.Key, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeVault) Source(key vault.Key, project string) (cred.CredentialSource, error) {
	f.capturedKey = key
	f.capturedProject = project
	if f.records != nil {
		rec, ok := f.records[key]
		if !ok {
			return nil, vault.ErrUnlinked
		}
		if !rec.AllowsProject(project) {
			return nil, vault.ErrProjectNotAllowed
		}
		return nil, nil
	}
	return nil, f.sourceErr
}
func (f *fakeVault) ForceRefresh(_ context.Context, key vault.Key) (vault.Record, error) {
	return vault.Record{}, errors.New("not implemented")
}

// TestParseGitHubOrigin verifies the URL parser.
func TestParseGitHubOrigin(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantOwner string
		wantRepo  string
		wantOK    bool
	}{
		// accepted forms
		{name: "ssh scp", raw: "git@github.com:acme/myrepo.git", wantOwner: "acme", wantRepo: "myrepo", wantOK: true},
		{name: "ssh scp no git suffix", raw: "git@github.com:acme/myrepo", wantOwner: "acme", wantRepo: "myrepo", wantOK: true},
		{name: "ssh:// form", raw: "ssh://git@github.com/acme/myrepo.git", wantOwner: "acme", wantRepo: "myrepo", wantOK: true},
		{name: "ssh:// form no git suffix", raw: "ssh://git@github.com/acme/myrepo", wantOwner: "acme", wantRepo: "myrepo", wantOK: true},
		{name: "https form", raw: "https://github.com/acme/myrepo.git", wantOwner: "acme", wantRepo: "myrepo", wantOK: true},
		{name: "https no git suffix", raw: "https://github.com/acme/myrepo", wantOwner: "acme", wantRepo: "myrepo", wantOK: true},
		{name: "https with user@", raw: "https://user@github.com/acme/myrepo.git", wantOwner: "acme", wantRepo: "myrepo", wantOK: true},
		{name: "trailing slash ssh scp", raw: "git@github.com:acme/myrepo.git/", wantOwner: "acme", wantRepo: "myrepo", wantOK: true},
		{name: "trailing slash https", raw: "https://github.com/acme/myrepo/", wantOwner: "acme", wantRepo: "myrepo", wantOK: true},
		{name: "mixed-case owner", raw: "git@github.com:AcMe/myrepo.git", wantOwner: "AcMe", wantRepo: "myrepo", wantOK: true},
		{name: "mixed-case host GitHub.com", raw: "git@GitHub.com:acme/myrepo.git", wantOwner: "acme", wantRepo: "myrepo", wantOK: true},

		// rejected forms — structural
		{name: "empty", raw: "", wantOK: false},
		{name: "gitlab.com", raw: "git@gitlab.com:acme/myrepo.git", wantOK: false},
		{name: "github.example.com", raw: "git@github.example.com:acme/myrepo.git", wantOK: false},
		{name: "api.github.com", raw: "https://api.github.com/acme/myrepo.git", wantOK: false},
		{name: "missing owner", raw: "https://github.com/acme", wantOK: false},
		{name: "missing repo ssh scp", raw: "git@github.com:acme/", wantOK: false},
		{name: "extra path segments", raw: "https://github.com/acme/repo/extra", wantOK: false},
		{name: "dot-dot segment", raw: "https://github.com/acme/../repo", wantOK: false},
		{name: "dot segment", raw: "https://github.com/./repo", wantOK: false},

		// rejected forms — wildcard injection (Fix 1)
		{name: "wildcard owner scp", raw: "git@github.com:*/*.git", wantOK: false},
		{name: "wildcard repo scp", raw: "git@github.com:acme/*.git", wantOK: false},
		{name: "double-star owner https", raw: "https://github.com/**/x", wantOK: false},
		{name: "percent-encoded wildcard https", raw: "https://github.com/%2A/%2A.git", wantOK: false},
		{name: "double-star repo https", raw: "https://github.com/acme/**", wantOK: false},
		{name: "query string https", raw: "https://github.com/acme/repo?q=1", wantOK: false},
		{name: "fragment https", raw: "https://github.com/acme/repo#frag", wantOK: false},
		{name: "scp with question mark", raw: "git@github.com:acme/re?po", wantOK: false},
		{name: "owner starting with dash", raw: "git@github.com:-acme/repo.git", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, ok := parseGitHubOrigin(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("parseGitHubOrigin(%q) ok=%v, want %v", tc.raw, ok, tc.wantOK)
			}
			if ok {
				if owner != tc.wantOwner {
					t.Errorf("owner=%q, want %q", owner, tc.wantOwner)
				}
				if repo != tc.wantRepo {
					t.Errorf("repo=%q, want %q", repo, tc.wantRepo)
				}
			}
		})
	}
}

// TestHerdrGitHubAutoBind verifies the pure binding logic.
func TestHerdrGitHubAutoBind(t *testing.T) {
	const origin = "git@github.com:acme/myrepo.git"

	// expectedPP is what we expect when a bind is added.
	expectedPP := func() domain.EgressPathPolicies {
		pp := domain.EgressPathPolicies{}
		pp = egressAddHostPolicy(pp, "github.com", domain.EgressHostPolicy{Paths: []string{"/acme/myrepo/**", "/acme/myrepo.git/**"}})
		pp = egressAddHostPolicy(pp, "api.github.com", domain.EgressHostPolicy{Paths: []string{"/repos/acme/myrepo/**", "/user"}})
		pp = egressAddHostPolicy(pp, "uploads.github.com", domain.EgressHostPolicy{Paths: []string{"/repos/acme/myrepo/**"}})
		return pp
	}

	t.Run("linked + github origin binds", func(t *testing.T) {
		outSecrets, outPP, added := herdrGitHubAutoBind(config.Config{}, origin, true, nil, nil)
		if !added {
			t.Fatal("expected added=true")
		}
		if len(outSecrets) != 1 || outSecrets[0] != herdrGitHubAutoBindSecret {
			t.Errorf("secrets=%v, want [%s]", outSecrets, herdrGitHubAutoBindSecret)
		}
		if !reflect.DeepEqual(outPP, expectedPP()) {
			t.Errorf("pathPolicies mismatch\ngot:  %#v\nwant: %#v", outPP, expectedPP())
		}
	})

	t.Run("unlinked gives no bind", func(t *testing.T) {
		outSecrets, outPP, added := herdrGitHubAutoBind(config.Config{}, origin, false, nil, nil)
		if added {
			t.Fatal("expected added=false for unlinked")
		}
		if outSecrets != nil || outPP != nil {
			t.Errorf("expected nil outputs, got secrets=%v pp=%v", outSecrets, outPP)
		}
	})

	t.Run("non-github origin gives no bind", func(t *testing.T) {
		outSecrets, outPP, added := herdrGitHubAutoBind(config.Config{}, "git@gitlab.com:acme/myrepo.git", true, nil, nil)
		if added {
			t.Fatal("expected added=false for non-github origin")
		}
		if outSecrets != nil || outPP != nil {
			t.Errorf("expected nil outputs")
		}
	})

	t.Run("wildcard origin gives no bind", func(t *testing.T) {
		outSecrets, outPP, added := herdrGitHubAutoBind(config.Config{}, "git@github.com:*/*.git", true, nil, nil)
		if added {
			t.Fatal("expected added=false for wildcard origin")
		}
		if outSecrets != nil || outPP != nil {
			t.Errorf("expected nil outputs")
		}
	})

	t.Run("config declares GH secret passes through unchanged", func(t *testing.T) {
		cfg := config.Config{}
		cfg.Egress.Secrets = config.EgressSecrets{
			{Env: "GH_TOKEN", Hosts: []string{"github.com"}},
		}
		cfg.Egress.Policy = config.EgressPolicies{
			{Host: "github.com", Paths: []string{"/repos/x/y/**"}},
		}
		existingSecrets := []string{"GH_TOKEN@github.com"}
		existingPP := domain.EgressPathPolicies{}
		existingPP = egressAddHostPolicy(existingPP, "github.com", domain.EgressHostPolicy{Paths: []string{"/repos/x/y/**"}})

		outSecrets, outPP, added := herdrGitHubAutoBind(cfg, origin, true, existingSecrets, existingPP)
		if added {
			t.Fatal("expected added=false when config declares GitHub")
		}
		if !reflect.DeepEqual(outSecrets, existingSecrets) {
			t.Errorf("secrets changed: got %v", outSecrets)
		}
		if !reflect.DeepEqual(outPP, existingPP) {
			t.Errorf("pp changed")
		}
	})

	t.Run("config with only a GitHub policy host counts as declared", func(t *testing.T) {
		cfg := config.Config{}
		cfg.Egress.Policy = config.EgressPolicies{
			{Host: "api.github.com", Paths: []string{"/repos/x/y/**"}},
		}
		outSecrets, outPP, added := herdrGitHubAutoBind(cfg, origin, true, nil, nil)
		if added {
			t.Fatal("expected added=false when config declares GitHub via policy")
		}
		if outSecrets != nil || outPP != nil {
			t.Errorf("expected nil outputs")
		}
	})

	t.Run("unrelated secret gets bind appended without mutating caller slice", func(t *testing.T) {
		callerSecrets := []string{"NPM_TOKEN@registry.npmjs.org"}
		outSecrets, _, added := herdrGitHubAutoBind(config.Config{}, origin, true, callerSecrets, nil)
		if !added {
			t.Fatal("expected added=true")
		}
		if len(outSecrets) != 2 {
			t.Fatalf("expected 2 secrets, got %v", outSecrets)
		}
		if outSecrets[0] != "NPM_TOKEN@registry.npmjs.org" {
			t.Errorf("first secret changed: %q", outSecrets[0])
		}
		if outSecrets[1] != herdrGitHubAutoBindSecret {
			t.Errorf("second secret wrong: %q", outSecrets[1])
		}
		if len(callerSecrets) != 1 {
			t.Errorf("caller slice was mutated: %v", callerSecrets)
		}
	})

	t.Run("no path equals /** and no graphql", func(t *testing.T) {
		_, outPP, added := herdrGitHubAutoBind(config.Config{}, origin, true, nil, nil)
		if !added {
			t.Fatal("expected bind")
		}
		for _, hostMap := range outPP {
			for host, pol := range hostMap {
				for _, p := range pol.Paths {
					if p == "/**" {
						t.Errorf("host %s: found forbidden root wildcard path /**", host)
					}
					if strings.Contains(p, "graphql") {
						t.Errorf("host %s: found forbidden graphql path: %s", host, p)
					}
				}
			}
		}
	})
}

// TestHerdrGitHubLinked verifies the vault-check helper.
func TestHerdrGitHubLinked(t *testing.T) {
	const principal = "slack:T1:U1"

	t.Run("nil vault is false", func(t *testing.T) {
		if herdrGitHubLinked(nil, principal, "") {
			t.Fatal("expected false for nil vault")
		}
	})

	t.Run("empty principal is false", func(t *testing.T) {
		fv := &fakeVault{}
		if herdrGitHubLinked(fv, "", "") {
			t.Fatal("expected false for empty principal")
		}
	})

	t.Run("ErrUnlinked is false", func(t *testing.T) {
		fv := &fakeVault{sourceErr: vault.ErrUnlinked}
		if herdrGitHubLinked(fv, principal, "") {
			t.Fatal("expected false for ErrUnlinked")
		}
	})

	t.Run("ErrProjectNotAllowed is false", func(t *testing.T) {
		fv := &fakeVault{sourceErr: vault.ErrProjectNotAllowed}
		if herdrGitHubLinked(fv, principal, "") {
			t.Fatal("expected false for ErrProjectNotAllowed")
		}
	})

	t.Run("any other error is false", func(t *testing.T) {
		fv := &fakeVault{sourceErr: errors.New("something else")}
		if herdrGitHubLinked(fv, principal, "") {
			t.Fatal("expected false for arbitrary error")
		}
	})

	t.Run("nil error is true", func(t *testing.T) {
		fv := &fakeVault{sourceErr: nil}
		if !herdrGitHubLinked(fv, principal, "") {
			t.Fatal("expected true when Source returns nil error")
		}
		if fv.capturedKey.Integration != "github" {
			t.Errorf("Integration=%q, want github", fv.capturedKey.Integration)
		}
		if fv.capturedKey.Principal != principal {
			t.Errorf("Principal=%q, want %s", fv.capturedKey.Principal, principal)
		}
	})

	t.Run("AllowedProjects enforced: projA allowed", func(t *testing.T) {
		key := vault.Key{Principal: principal, Integration: "github"}
		fv := &fakeVault{records: map[vault.Key]vault.Record{
			key: {AllowedProjects: []string{"projA"}},
		}}
		if !herdrGitHubLinked(fv, principal, "projA") {
			t.Fatal("expected true for projA")
		}
	})

	t.Run("AllowedProjects enforced: empty project rejected", func(t *testing.T) {
		key := vault.Key{Principal: principal, Integration: "github"}
		fv := &fakeVault{records: map[vault.Key]vault.Record{
			key: {AllowedProjects: []string{"projA"}},
		}}
		if herdrGitHubLinked(fv, principal, "") {
			t.Fatal("expected false for empty project when AllowedProjects=[projA]")
		}
	})

	t.Run("AllowedProjects enforced: projB rejected", func(t *testing.T) {
		key := vault.Key{Principal: principal, Integration: "github"}
		fv := &fakeVault{records: map[vault.Key]vault.Record{
			key: {AllowedProjects: []string{"projA"}},
		}}
		if herdrGitHubLinked(fv, principal, "projB") {
			t.Fatal("expected false for projB when AllowedProjects=[projA]")
		}
	})

	t.Run("project passed to Source equals argument", func(t *testing.T) {
		key := vault.Key{Principal: principal, Integration: "github"}
		fv := &fakeVault{records: map[vault.Key]vault.Record{
			key: {AllowedProjects: []string{"projA"}},
		}}
		herdrGitHubLinked(fv, principal, "projA")
		if fv.capturedProject != "projA" {
			t.Errorf("Source received project=%q, want projA", fv.capturedProject)
		}
	})
}

// TestHerdrApplyGitHubAutoBind verifies the call-site wrapper.
func TestHerdrApplyGitHubAutoBind(t *testing.T) {
	ctx := context.Background()
	const checkoutPath = "/tmp/fake-checkout"

	saveOriginFn := herdrOriginURLFn
	saveLinkedFn := herdrGitHubLinkedFn
	t.Cleanup(func() {
		herdrOriginURLFn = saveOriginFn
		herdrGitHubLinkedFn = saveLinkedFn
	})

	t.Run("declared config: linkedFn not called", func(t *testing.T) {
		t.Setenv(vault.PrincipalEnv, "slack:T1:U1")

		cfg := config.Config{}
		cfg.Egress.Secrets = config.EgressSecrets{
			{Env: "GH_TOKEN", Hosts: []string{"github.com"}},
		}

		linkedCalled := false
		herdrGitHubLinkedFn = func(principal, project string) (bool, error) {
			linkedCalled = true
			return true, nil
		}
		herdrOriginURLFn = func(_ context.Context, _ string) string {
			return "git@github.com:acme/repo.git"
		}

		var buf bytes.Buffer
		inSecrets := []string{"GH_TOKEN@github.com"}
		inPP := domain.EgressPathPolicies{}
		outSecrets, outPP := herdrApplyGitHubAutoBind(ctx, &buf, checkoutPath, "", cfg, inSecrets, inPP)
		if linkedCalled {
			t.Fatal("linkedFn must not be called when config declares GitHub")
		}
		if !reflect.DeepEqual(outSecrets, inSecrets) {
			t.Errorf("secrets changed: %v", outSecrets)
		}
		_ = outPP
	})

	t.Run("empty principal: linkedFn not called", func(t *testing.T) {
		t.Setenv(vault.PrincipalEnv, "")

		linkedCalled := false
		herdrGitHubLinkedFn = func(principal, project string) (bool, error) {
			linkedCalled = true
			return true, nil
		}
		herdrOriginURLFn = func(_ context.Context, _ string) string {
			return "git@github.com:acme/repo.git"
		}

		var buf bytes.Buffer
		inSecrets := []string{"existing"}
		inPP := domain.EgressPathPolicies{}
		outSecrets, outPP := herdrApplyGitHubAutoBind(ctx, &buf, checkoutPath, "", config.Config{}, inSecrets, inPP)
		if linkedCalled {
			t.Fatal("linkedFn must not be called when principal is empty")
		}
		if !reflect.DeepEqual(outSecrets, inSecrets) {
			t.Errorf("secrets changed: %v", outSecrets)
		}
		if !reflect.DeepEqual(outPP, inPP) {
			t.Errorf("pp changed")
		}
	})

	t.Run("linkedFn error gives vault unavailable line and no bind", func(t *testing.T) {
		t.Setenv(vault.PrincipalEnv, "slack:T1:U1")

		herdrOriginURLFn = func(_ context.Context, _ string) string {
			return "git@github.com:acme/repo.git"
		}
		herdrGitHubLinkedFn = func(principal, project string) (bool, error) {
			return false, errors.New("vault open failed")
		}

		var buf bytes.Buffer
		outSecrets, outPP := herdrApplyGitHubAutoBind(ctx, &buf, checkoutPath, "", config.Config{}, nil, nil)
		if !strings.Contains(buf.String(), "vault unavailable") {
			t.Errorf("expected vault unavailable message, got: %q", buf.String())
		}
		if outSecrets != nil || outPP != nil {
			t.Errorf("expected no bind, got secrets=%v pp=%v", outSecrets, outPP)
		}
	})

	t.Run("success writes scoped to O/R line", func(t *testing.T) {
		t.Setenv(vault.PrincipalEnv, "slack:T1:U1")

		herdrOriginURLFn = func(_ context.Context, _ string) string {
			return "git@github.com:AcMe/myrepo.git"
		}
		herdrGitHubLinkedFn = func(principal, project string) (bool, error) {
			return true, nil
		}

		var buf bytes.Buffer
		outSecrets, _ := herdrApplyGitHubAutoBind(ctx, &buf, checkoutPath, "", config.Config{}, nil, nil)
		if !strings.Contains(buf.String(), "AcMe/myrepo") {
			t.Errorf("expected scoped to AcMe/myrepo in output, got: %q", buf.String())
		}
		if len(outSecrets) == 0 {
			t.Fatal("expected at least one secret after bind")
		}
	})

	t.Run("project passed to linkedFn", func(t *testing.T) {
		t.Setenv(vault.PrincipalEnv, "slack:T1:U1")

		herdrOriginURLFn = func(_ context.Context, _ string) string {
			return "git@github.com:acme/repo.git"
		}
		var gotProject string
		herdrGitHubLinkedFn = func(principal, project string) (bool, error) {
			gotProject = project
			return true, nil
		}

		var buf bytes.Buffer
		herdrApplyGitHubAutoBind(ctx, &buf, checkoutPath, "projA", config.Config{}, nil, nil)
		if gotProject != "projA" {
			t.Errorf("linkedFn received project=%q, want projA", gotProject)
		}
	})
}
