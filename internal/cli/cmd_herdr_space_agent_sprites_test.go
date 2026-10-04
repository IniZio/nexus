package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/registry"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
)

type spritesProbeSvc struct {
	sb   domain.Sandbox
	code int32
	out  string
	err  error
	argv []string
}

func (s *spritesProbeSvc) Get(context.Context, string) (domain.Sandbox, error) { return s.sb, nil }

func (s *spritesProbeSvc) Exec(_ context.Context, _ string, o agent.ExecOptions) (int32, error) {
	s.argv = o.Argv
	if o.Stdout != nil {
		_, _ = io.WriteString(o.Stdout, s.out)
	}
	return s.code, s.err
}

func TestSpaceAgentProjectDir_SpritesWorkTree(t *testing.T) {
	svc := &spritesProbeSvc{sb: domain.Sandbox{Backend: registry.Sprites}, out: "true\n"}
	dir, err := herdrSpaceAgentProjectDir(context.Background(), "repo/x", svc)
	if err != nil || dir != sprites.CloneDir {
		t.Fatalf("dir=%q err=%v, want %q", dir, err, sprites.CloneDir)
	}
	want := []string{"git", "-C", sprites.CloneDir, "rev-parse", "--is-inside-work-tree"}
	if strings.Join(svc.argv, " ") != strings.Join(want, " ") {
		t.Fatalf("probe argv = %q, want %q", svc.argv, want)
	}
}

func TestSpaceAgentProjectDir_SpritesNotWorkTree(t *testing.T) {
	for name, svc := range map[string]*spritesProbeSvc{
		"nonzero": {sb: domain.Sandbox{Backend: registry.Sprites}, code: 128},
		"exec":    {sb: domain.Sandbox{Backend: registry.Sprites}, err: errors.New("boom")},
		"false":   {sb: domain.Sandbox{Backend: registry.Sprites}, out: "false\n"},
	} {
		_, err := herdrSpaceAgentProjectDir(context.Background(), "repo/x", svc)
		var ue *UsageError
		if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "no git work tree at "+sprites.CloneDir) {
			t.Errorf("%s: err = %v", name, err)
		}
		if strings.Contains(ue.Msg, "--mount") {
			t.Errorf("%s: sprites refusal must not advise --mount: %q", name, ue.Msg)
		}
	}
}

func TestSpaceAgentProjectDir_CHStillNeedsMount(t *testing.T) {
	svc := &spritesProbeSvc{sb: domain.Sandbox{Backend: registry.CloudHypervisor}, out: "true"}
	if _, err := herdrSpaceAgentProjectDir(context.Background(), "repo/x", svc); err == nil || svc.argv != nil {
		t.Fatalf("CH record without mount must refuse without probing; err=%v argv=%v", err, svc.argv)
	}
}

func TestWithAgentModel(t *testing.T) {
	base := guestAgentLaunchCommand(true)
	if got, err := withAgentModel(base, ""); err != nil || got != base {
		t.Fatalf("empty model changes launch: %q %v", got, err)
	}
	got, err := withAgentModel(base, "haiku")
	if err != nil || got != base+" --model 'haiku'" {
		t.Fatalf("got %q %v", got, err)
	}
	if got, err = withAgentModel(base, "opus[1m]"); err != nil || !strings.HasSuffix(got, "--model 'opus[1m]'") {
		t.Fatalf("got %q %v", got, err)
	}
	for _, bad := range []string{"a b", "x'; rm -rf /", "$(id)", "-x", "a\nb"} {
		if _, err := withAgentModel(base, bad); err == nil {
			t.Errorf("model %q accepted", bad)
		}
	}
}

func TestSpaceAgent_ModelFlagRequiresValue(t *testing.T) {
	out := NewOutput(io.Discard, io.Discard, false)
	err := runHerdrPlugin(context.Background(), []string{"space-agent", "--model"}, out)
	var ue *UsageError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "--model requires a value") {
		t.Fatalf("err = %v", err)
	}
}
