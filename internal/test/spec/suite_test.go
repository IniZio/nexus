// Package spec contains a Gherkin-driven harness that mechanically reconciles
// the product-manual badge states (built / partial / not-built) against the
// live Go code.
//
// Badge semantics enforced by this suite:
//
//	@badge-built     — capability is fully implemented; a failure means regression.
//	@badge-partial   — capability is partially built; a PASS means the badge is stale.
//	@badge-not-built — capability is unbuilt; a PASS means the badge is stale.
//
// Stale-badge detection: the After hook fires when a @badge-partial or
// @badge-not-built scenario completes without any step returning ErrPending.
// That means the documented gap is now closed and the badge must be updated.
// Steps that probe real production code set specCtx.hadPending = true only on
// the not-found branch so the After hook can distinguish "all steps passed
// (badge stale)" from "a step was pending (badge still accurate)".
// Steps must probe real production symbols — returning ErrPending
// unconditionally makes the hook permanently unreachable and defeats the check.
//
// NOTE on godog's After-hook err parameter: godog passes the LAST STEP's
// individual error to AfterScenarioHook, not the accumulated scenarioErr.
// When a middle step is pending and the final step is skipped, lastStepErr is
// nil even though the scenario is pending. We therefore cannot rely solely on
// err == nil to detect "all steps passed"; we also require !hadPending.
//
// Component wiring is provided by internal/test/harness so both this suite
// and internal/test/acceptance share the same seam without duplication.
//
// Run with: TMPDIR=/tmp go test ./internal/test/spec/
package spec_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/IniZio/nexus3/internal/cli"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	testharness "github.com/IniZio/nexus3/internal/test/harness"
)

// ── per-scenario state ────────────────────────────────────────────────────────

type specCtx struct {
	svc        *service.Service
	st         *store.FileStore
	listLen    int
	cmdFound   bool
	hadPending bool // set by any step that explicitly returns (or wraps) ErrPending
	seededID   string
	lastErr    error
}

type ctxKey struct{}

// ── scenario initializer ──────────────────────────────────────────────────────

func InitializeScenario(sc *godog.ScenarioContext) {
	// Before each scenario: wire a fresh harness via the shared seam.
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		root, err := os.MkdirTemp("", "nexus3-spec-*")
		if err != nil {
			return ctx, fmt.Errorf("MkdirTemp: %w", err)
		}
		h, err := testharness.New(root)
		if err != nil {
			return ctx, fmt.Errorf("harness.New: %w", err)
		}
		return context.WithValue(ctx, ctxKey{}, &specCtx{svc: h.Svc, st: h.St}), nil
	})

	// After each scenario: enforce stale-badge invariant.
	//
	// A @badge-partial or @badge-not-built scenario where no step returned
	// ErrPending and no step returned a non-pending error (err == nil AND
	// !hadPending) means every step passed — the documented gap is now closed
	// and the badge is stale. The suite must go RED in that case.
	//
	// godog passes lastStepErr (the skipped final step's nil) rather than the
	// accumulated scenarioErr, so we cannot use `err != nil` alone to detect
	// pending; we read hadPending from the per-scenario context instead.
	sc.After(func(ctx context.Context, scenario *godog.Scenario, err error) (context.Context, error) {
		if !isUnbuiltTag(scenario) {
			// @badge-built scenario: leave err untouched — pass means pass,
			// fail means fail.
			return ctx, nil
		}
		s, _ := ctx.Value(ctxKey{}).(*specCtx)
		if err == nil && (s == nil || !s.hadPending) {
			// All steps passed on a badge-partial / badge-not-built scenario
			// with no step having gone pending — the badge in the docs is stale.
			return ctx, fmt.Errorf(
				"stale-badge: scenario %q (%v) passed unexpectedly — "+
					"update the badge in the product manual to reflect the built capability",
				scenario.Name, scenarioTagNames(scenario),
			)
		}
		// err != nil OR hadPending: known divergence — keep suite green.
		_ = errors.Is(err, godog.ErrPending) // suppress staticcheck "errors.Is always false"
		return ctx, nil
	})

	registerSteps(sc)
}

// isUnbuiltTag reports whether scenario carries @badge-partial or @badge-not-built.
func isUnbuiltTag(sc *godog.Scenario) bool {
	for _, t := range sc.Tags {
		if t.Name == "@badge-partial" || t.Name == "@badge-not-built" {
			return true
		}
	}
	return false
}

// scenarioTagNames returns the tag names as a string slice for error messages.
func scenarioTagNames(sc *godog.Scenario) []string {
	names := make([]string, 0, len(sc.Tags))
	for _, t := range sc.Tags {
		names = append(names, t.Name)
	}
	return names
}

// ── step definitions ──────────────────────────────────────────────────────────

func registerSteps(sc *godog.ScenarioContext) {

	// ── Scenario 1: BUILT ─────────────────────────────────────────────────
	// Docs:    docs/site/cli/sandbox-commands.md — sandbox list section.
	//          No danger/warning badge → capability is fully built.
	// Driver:  service.List on a fresh FileStore.

	sc.Step(`^a fresh sandbox store$`, func() error {
		return nil // harness already fresh per scenario — step is documentary
	})

	sc.Step(`^I list all sandboxes$`, func(gctx context.Context) error {
		s := gctx.Value(ctxKey{}).(*specCtx)
		sandboxes, err := s.svc.List(context.Background())
		if err != nil {
			return fmt.Errorf("service.List: %w", err)
		}
		s.listLen = len(sandboxes)
		return nil
	})

	sc.Step(`^the list is empty$`, func(gctx context.Context) error {
		s := gctx.Value(ctxKey{}).(*specCtx)
		if s.listLen != 0 {
			return fmt.Errorf("expected empty list, got %d sandboxes", s.listLen)
		}
		return nil
	})

	// ── Scenario 2: PARTIAL (warning) ─────────────────────────────────────
	// Docs:    docs/site/ai-agents.md:74
	//            <Badge type="warning" text="partial" /> — target design exposes
	//            `nexus3 create` as a top-level verb; current impl uses
	//            `nexus3 sandbox create`.
	// Driver:  cli.Lookup — checks the in-process command registry.
	// Outcome: PENDING — the capability is partially built; the step reports
	//          divergence as pending so the suite stays green. The After hook
	//          turns this red if the command is ever registered (stale badge).

	sc.Step(`^the nexus3 CLI$`, func() error {
		return nil // CLI registry loaded via the cli import side-effect
	})

	sc.Step(`^I look up the command "([^"]*)"$`, func(gctx context.Context, name string) error {
		s := gctx.Value(ctxKey{}).(*specCtx)
		_, s.cmdFound = cli.Lookup(name)
		return nil
	})

	sc.Step(`^the command is registered$`, func(gctx context.Context) (context.Context, error) {
		s := gctx.Value(ctxKey{}).(*specCtx)
		if !s.cmdFound {
			// Known divergence: target design uses `nexus3 create`; current
			// impl registers it as `nexus3 sandbox create` (docs badge: partial).
			s.hadPending = true
			return gctx, fmt.Errorf(
				"'create' is not a registered top-level command — "+
					"current impl uses 'nexus3 sandbox create' "+
					"(docs/site/ai-agents.md:74, badge: partial): %w",
				godog.ErrPending,
			)
		}
		return gctx, nil
	})

	// ── Scenario 3: NOT BUILT (danger) ────────────────────────────────────
	// Docs:    docs/site/ai-agents.md:96
	//            ### Public agent launch surface
	//            <Badge type="danger" text="not built" />
	// Outcome: PENDING — the "agent" verb is absent from the CLI registry, so
	//          the second step sets hadPending and returns ErrPending, keeping
	//          the suite exit-code green. When someone registers the "agent"
	//          verb, the step returns nil, the After hook fires, and the suite
	//          goes RED — signalling that the badge must be updated to "built".

	sc.Step(`^an agent requests a launch for task "([^"]*)"$`,
		func(gctx context.Context, _ string) (context.Context, error) {
			s := gctx.Value(ctxKey{}).(*specCtx)
			_, s.cmdFound = cli.Lookup("agent")
			return gctx, nil
		})

	sc.Step(`^a new sandbox is created for the agent task$`,
		func(gctx context.Context) (context.Context, error) {
			s := gctx.Value(ctxKey{}).(*specCtx)
			if !s.cmdFound {
				// Known gap: public agent launch surface not yet built.
				// docs/site/ai-agents.md:96, badge: not-built.
				s.hadPending = true
				return gctx, fmt.Errorf(
					"'agent' is not a registered command — "+
						"public agent launch surface not yet built "+
						"(docs/site/ai-agents.md:96, badge: not-built): %w",
					godog.ErrPending,
				)
			}
			return gctx, nil
		})

	// ── Scenarios 4–5: BUILT — D-PD-53 live-mount guard ──────────────────
	// Docs:    docs/site/cli/sandbox-commands.md (fork / snapshot sections).
	//          No danger/warning badge → capability is fully built.
	// Scenario 4: refusal path — a sandbox with a live host-directory mount
	//             must be refused by both Fork and Snapshot, citing D-PD-53.
	// Scenario 5: negative control — a sandbox with no live mounts must NOT
	//             trigger the D-PD-53 guard (proves the guard is not over-broad).

	sc.Step(`^a sandbox with a live host-directory mount exists in the store$`,
		func(gctx context.Context) error {
			s := gctx.Value(ctxKey{}).(*specCtx)
			sb := domain.Sandbox{
				ID:         domain.NewSandboxID(),
				Name:       "sb-live-mount",
				Project:    "spec-d-pd-53",
				State:      domain.Running,
				Envelope:   domain.Envelope{ImageDigest: "sha256:spec-d-pd-53"},
				InstanceID: "inst-spec-d-pd-53",
				LiveMounts: []domain.LiveMount{
					{HostPath: "/spec/host/project", GuestPath: "/workspace", ReadOnly: false},
				},
			}
			if err := s.st.Create(context.Background(), sb); err != nil {
				return fmt.Errorf("seed sandbox with live mount: %w", err)
			}
			s.seededID = sb.ID.String()
			return nil
		})

	sc.Step(`^a sandbox with no live mounts exists in the store$`,
		func(gctx context.Context) error {
			s := gctx.Value(ctxKey{}).(*specCtx)
			sb := domain.Sandbox{
				ID:         domain.NewSandboxID(),
				Name:       "sb-no-live-mounts",
				Project:    "spec-d-pd-53-clean",
				State:      domain.Running,
				Envelope:   domain.Envelope{ImageDigest: "sha256:spec-d-pd-53-clean"},
				InstanceID: "inst-spec-d-pd-53-clean",
				LiveMounts: nil,
			}
			if err := s.st.Create(context.Background(), sb); err != nil {
				return fmt.Errorf("seed sandbox with no live mounts: %w", err)
			}
			s.seededID = sb.ID.String()
			return nil
		})

	sc.Step(`^I fork the sandbox$`, func(gctx context.Context) error {
		s := gctx.Value(ctxKey{}).(*specCtx)
		_, s.lastErr = s.svc.Fork(context.Background(), s.seededID, 1)
		return nil
	})

	sc.Step(`^I snapshot the sandbox$`, func(gctx context.Context) error {
		s := gctx.Value(ctxKey{}).(*specCtx)
		_, s.lastErr = s.svc.Snapshot(context.Background(), s.seededID)
		return nil
	})

	sc.Step(`^the error cites "([^"]*)"$`, func(gctx context.Context, marker string) error {
		s := gctx.Value(ctxKey{}).(*specCtx)
		if s.lastErr == nil {
			return fmt.Errorf("expected an error citing %q but got nil", marker)
		}
		if !strings.Contains(s.lastErr.Error(), marker) {
			return fmt.Errorf("error does not cite %q: %v", marker, s.lastErr)
		}
		return nil
	})

	sc.Step(`^the error does not cite "([^"]*)"$`, func(gctx context.Context, marker string) error {
		s := gctx.Value(ctxKey{}).(*specCtx)
		if s.lastErr != nil && strings.Contains(s.lastErr.Error(), marker) {
			return fmt.Errorf("unexpected %q refusal: %v", marker, s.lastErr)
		}
		return nil
	})
}

// ── test entry point ──────────────────────────────────────────────────────────

func TestFeatures(t *testing.T) {
	// godog.TestSuite runs each scenario as a sub-test under t.
	// Expected outcomes:
	//   @badge-built      → PASS  (service.List works)
	//   @badge-partial    → PENDING (create not top-level; After hook suppresses)
	//   @badge-not-built  → PENDING (ErrPending; After hook suppresses)
	// Stale-badge signal: if @badge-partial / @badge-not-built unexpectedly
	// pass without any step going pending, the After hook returns an error
	// and the sub-test turns RED.
	godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			TestingT: t,
		},
	}.Run()
}
