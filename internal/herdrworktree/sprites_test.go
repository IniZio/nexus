package herdrworktree

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

type fakeSyncer struct {
	status    string
	notAtSeed bool
	statusErr error
	exportErr error
	exported  []string
	mode      string
	unpushed  string
}

func (f *fakeSyncer) SyncMode(domain.SandboxID) (string, error) { return f.mode, nil }
func (f *fakeSyncer) GuestUnpushed(context.Context, domain.SandboxID, string) (string, error) {
	return f.unpushed, nil
}

func TestTeardownPushMode(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/bin/true")
	for name, tc := range map[string]struct {
		s     *fakeSyncer
		force bool
		want  bool
	}{
		"pushed":      {&fakeSyncer{mode: "push"}, false, true},
		"unpushed":    {&fakeSyncer{mode: "push", unpushed: "2 commit(s) not pushed"}, false, false},
		"uncommitted": {&fakeSyncer{mode: "push", status: " M a\n"}, false, false},
		"force":       {&fakeSyncer{mode: "push", unpushed: "x", status: "y"}, true, true},
		"bundle":      {&fakeSyncer{mode: "bundle", unpushed: "ignored"}, false, true},
	} {
		var calls []string
		r := spriteRunners(tc.s, true, &calls)
		r.PollTimeout = 1
		_, err := Teardown(context.Background(), "p/a", tc.force, r)
		if tc.want != (err == nil) || tc.want != destroyed(calls) {
			t.Fatalf("%s: err=%v calls=%v", name, err, calls)
		}
		if len(tc.s.exported) != 0 && tc.s.mode == "push" {
			t.Fatalf("%s: push mode exported to host", name)
		}
		if !tc.want {
			var ue *SpriteUnsyncedError
			if !errors.As(err, &ue) {
				t.Fatalf("%s: err=%v", name, err)
			}
		}
	}
}

func TestTeardownUnboundPushMode(t *testing.T) {
	var calls []string
	s := &fakeSyncer{mode: "push", notAtSeed: true}
	if _, err := Teardown(context.Background(), "p/a", false, unboundRunners(s, true, &calls)); err != nil {
		t.Fatalf("pushed push-mode sprite refused: %v", err)
	}
	s = &fakeSyncer{mode: "push", unpushed: "1 commit(s) not pushed"}
	var ue *SpriteUnsyncedError
	if _, err := Teardown(context.Background(), "p/a", false, unboundRunners(s, true, &calls)); !errors.As(err, &ue) {
		t.Fatalf("err = %v", err)
	}
}

func (f *fakeSyncer) ExportWorktree(_ context.Context, _ domain.SandboxID, guest, host, branch string) (string, error) {
	f.exported = append(f.exported, guest+"|"+host+"|"+branch)
	return "sha", f.exportErr
}

func (f *fakeSyncer) GuestStatus(context.Context, domain.SandboxID, string) (string, error) {
	return f.status, f.statusErr
}

const spriteListOut = "label=l\tworkspace_id=w1\thandle=p/a\tsandbox_id=sb-x\tpane_id=\n"

func spriteRunners(s *fakeSyncer, isSprite bool, calls *[]string) Runners {
	return Runners{
		Herdr: func(_ context.Context, _ string, argv ...string) (string, error) {
			*calls = append(*calls, "herdr "+strings.Join(argv, " "))
			if argv[0] == "worktree" && argv[1] == "list" {
				return `{"result":{"worktrees":[{"path":"/wt","open_workspace_id":"w1"}]}}`, nil
			}
			return "ok", nil
		},
		Host: func(_ context.Context, argv ...string) (string, error) {
			*calls = append(*calls, "host "+strings.Join(argv, " "))
			if argv[0] == "herdr" {
				return spriteListOut, nil
			}
			return "", nil
		},
		Git: func(context.Context, ...string) (string, error) { return "feat\n", nil },
		SpriteSync: func(context.Context, string) (SpriteSyncer, domain.SandboxID, bool, error) {
			return s, domain.SandboxID{}, isSprite, nil
		},
	}
}

func destroyed(calls []string) bool {
	for _, c := range calls {
		if strings.HasPrefix(c, "herdr worktree remove") {
			return true
		}
	}
	return false
}

func TestTeardownSpriteExportsThenDestroys(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/bin/true")
	s := &fakeSyncer{}
	var calls []string
	r := spriteRunners(s, true, &calls)
	r.PollTimeout = 1
	if _, err := Teardown(context.Background(), "p/a", false, r); err != nil {
		t.Fatal(err)
	}
	if len(s.exported) != 1 || s.exported[0] != "/home/sprite/work|/wt|feat" {
		t.Fatalf("exported = %v", s.exported)
	}
	if !destroyed(calls) {
		t.Fatalf("not destroyed: %v", calls)
	}
}

func TestTeardownSpriteRefuses(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/bin/true")
	for name, s := range map[string]*fakeSyncer{
		"non-ff":      {exportErr: errors.New("not a fast-forward")},
		"uncommitted": {status: " M a.go\n"},
		"status-err":  {statusErr: errors.New("boom")},
	} {
		var calls []string
		_, err := Teardown(context.Background(), "p/a", false, spriteRunners(s, true, &calls))
		var ue *SpriteUnsyncedError
		if !errors.As(err, &ue) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if destroyed(calls) {
			t.Fatalf("%s: destroyed despite refusal", name)
		}
	}
}

func TestTeardownSpriteForceDestroys(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/bin/true")
	s := &fakeSyncer{exportErr: errors.New("not a fast-forward"), status: " M a.go\n"}
	var calls []string
	r := spriteRunners(s, true, &calls)
	r.PollTimeout = 1
	if _, err := Teardown(context.Background(), "p/a", true, r); err != nil {
		t.Fatal(err)
	}
	if !destroyed(calls) {
		t.Fatalf("not destroyed: %v", calls)
	}
}

func TestTeardownNonSpriteSkipsGuard(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", "/bin/true")
	s := &fakeSyncer{exportErr: errors.New("x"), status: "dirty"}
	var calls []string
	r := spriteRunners(s, false, &calls)
	r.PollTimeout = 1
	if _, err := Teardown(context.Background(), "p/a", false, r); err != nil {
		t.Fatal(err)
	}
	if len(s.exported) != 0 || !destroyed(calls) {
		t.Fatalf("exported=%v calls=%v", s.exported, calls)
	}
}

func (f *fakeSyncer) GuestAtSeed(context.Context, domain.SandboxID, string) (bool, error) {
	return !f.notAtSeed, nil
}

func unboundRunners(s *fakeSyncer, isSprite bool, calls *[]string) Runners {
	return Runners{
		Host: func(_ context.Context, argv ...string) (string, error) {
			*calls = append(*calls, strings.Join(argv, " "))
			if argv[0] == "herdr" {
				return "", nil
			}
			if argv[1] == "list" {
				return "p/a sb-abc running\n", nil
			}
			return "ok", nil
		},
		SpriteSync: func(context.Context, string) (SpriteSyncer, domain.SandboxID, bool, error) {
			return s, domain.SandboxID{}, isSprite, nil
		},
	}
}

func removed(calls []string) bool {
	for _, c := range calls {
		if c == "sandbox rm p/a" {
			return true
		}
	}
	return false
}

func TestTeardownUnboundSprite(t *testing.T) {
	for name, tc := range map[string]struct {
		s      *fakeSyncer
		sprite bool
		force  bool
		want   bool
	}{
		"clean":       {&fakeSyncer{}, true, false, true},
		"commits":     {&fakeSyncer{notAtSeed: true}, true, false, false},
		"uncommitted": {&fakeSyncer{status: " M a\n"}, true, false, false},
		"force":       {&fakeSyncer{notAtSeed: true, status: "x"}, true, true, true},
		"non-sprite":  {&fakeSyncer{notAtSeed: true, status: "x"}, false, false, true},
	} {
		var calls []string
		_, err := Teardown(context.Background(), "p/a", tc.force, unboundRunners(tc.s, tc.sprite, &calls))
		if tc.want != (err == nil) || tc.want != removed(calls) {
			t.Fatalf("%s: err=%v calls=%v", name, err, calls)
		}
		if !tc.want {
			var ue *SpriteUnsyncedError
			if !errors.As(err, &ue) || !strings.Contains(err.Error(), "p/a") {
				t.Fatalf("%s: err=%v", name, err)
			}
		}
	}
}
