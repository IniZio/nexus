package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// herdrAdoptGetter is the subset of *service.Service herdrSpaceResolveOrAdopt
// needs. Narrow on purpose so tests do not have to stand up a full service.
type herdrAdoptGetter interface {
	Get(ctx context.Context, ref string) (domain.Sandbox, error)
}

// herdrSpaceResolveOrAdopt resolves a space binding and, failing that, ADOPTS
// the sandbox by minting one.
//
// # Why adoption exists (TBR-PD-20)
//
// Listing a sandbox in herdr never needed a binding: `workspaces` calls
// svc.List unfiltered, so every sandbox shows up however it was created. But
// every ACTION — open-pane, pause, resume, remove — went through
// herdrSpaceResolve and dead-ended with "binding not found" for any sandbox
// created outside herdr. A sandbox made with `nexus3 sandbox create` was
// therefore visible and inert, which is the worst of both: it looks
// controllable and is not.
//
// Adoption is deliberately LAZY rather than eager. Binding at create time
// would mint a herdr workspace for every sandbox, including the throwaway ones
// that `nexus3 run` and the test suites produce, and would make core create
// depend on an external UI being installed. Doing it at first use costs
// nothing until someone actually asks herdr to act on the sandbox, and by
// definition herdr is running at that moment.
//
// # No herdr workspace is created here
//
// The adopted binding carries an EMPTY HerdrWorkspaceID. Pause, resume and
// remove only need the sandbox handle, and remove's herdrWorkspaceClose is a
// no-op on an empty ID. Only open-pane needs a real workspace, and it mints
// one lazily via herdrSpaceEnsureWorkspace. Minting one here would create
// empty workspaces every time someone paused a sandbox.
//
// Returns the binding and whether it was adopted (as opposed to found).
func herdrSpaceResolveOrAdopt(
	ctx context.Context,
	svc herdrAdoptGetter,
	storeRoot, key string,
) (HerdrSpaceBinding, bool, error) {
	b, err := herdrSpaceResolve(ctx, storeRoot, key)
	if err == nil {
		return b, false, nil
	}
	if !errors.Is(err, ErrHerdrSpaceNotFound) {
		return HerdrSpaceBinding{}, false, err
	}

	// No binding. The key may still name a real sandbox — that is exactly the
	// "created outside herdr" case. Resolve it through the service, which
	// accepts a handle, an ID, or an ID prefix.
	sb, getErr := svc.Get(ctx, key)
	if getErr != nil {
		return HerdrSpaceBinding{}, false, fmt.Errorf(
			"no space binding for %q, and no sandbox by that name: %w", key, getErr)
	}

	adopted := HerdrSpaceBinding{
		SpaceLabel:    herdrSpaceLabelForRef(sb.Handle()),
		SandboxHandle: sb.Handle(),
		SandboxID:     sb.ID.String(),
		// HerdrWorkspaceID intentionally empty — see the doc comment.
	}
	if putErr := HerdrSpacePut(ctx, storeRoot, adopted); putErr != nil {
		return HerdrSpaceBinding{}, false, fmt.Errorf("adopt sandbox %q: %w", sb.Handle(), putErr)
	}
	return adopted, true, nil
}

// herdrSpaceEnsureWorkspace returns b with a real HerdrWorkspaceID, creating
// the herdr workspace and persisting the updated binding if it had none.
//
// Only pane-opening needs this. Splitting it out from adoption is what keeps
// `space-pause` from leaving an empty herdr workspace behind every time it
// touches a sandbox that herdr did not create.
//
// The second return value is the new workspace's root pane ID, non-empty
// only when this call actually minted a workspace (as opposed to reusing an
// existing one). The caller grafts the guest pane onto that root pane instead
// of opening a second tab — see herdrOpenGuestShellPane.
//
// Orphan window (TBD-SHL-7): if HerdrSpacePut fails after herdrWorkspaceCreate
// has already returned a workspace ID, the live herdr workspace is permanently
// orphaned — its ID is not recorded in any binding and cannot be reached or
// reaped by space-prune.
func herdrSpaceEnsureWorkspace(
	ctx context.Context,
	svc herdrAdoptGetter,
	storeRoot, herdrBin string,
	b HerdrSpaceBinding,
) (HerdrSpaceBinding, string, error) {
	if b.HerdrWorkspaceID != "" {
		return b, "", nil
	}
	if herdrBin == "" {
		return b, "", fmt.Errorf("cannot create a herdr workspace for %q: no herdr binary (HERDR_BIN_PATH unset and none on PATH)", b.SandboxHandle)
	}
	hostCwd := herdrShellHostCwd(ctx, b.SandboxHandle, svc)
	id, rootPaneID, err := herdrWorkspaceCreate(ctx, herdrBin, b.SpaceLabel, hostCwd)
	if err != nil {
		return b, "", fmt.Errorf("herdr workspace create for %q: %w", b.SpaceLabel, err)
	}
	b.HerdrWorkspaceID = id
	if err := HerdrSpacePut(ctx, storeRoot, b); err != nil {
		return b, "", fmt.Errorf("store binding for %q: %w", b.SpaceLabel, err)
	}
	return b, rootPaneID, nil
}

// herdrAdoptNotice tells the operator on stderr that a sandbox was adopted, so
// a binding never appears out of nowhere. stderr, not stdout, because several
// of these subcommands have machine-readable stdout.
func herdrAdoptNotice(b HerdrSpaceBinding) {
	fmt.Fprintf(os.Stderr, "nexus3: adopted sandbox %s into herdr as %s\n",
		b.SandboxHandle, b.SpaceLabel)
}
