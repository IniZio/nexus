package cli

import (
	"context"
	"strings"
	"testing"
)

func TestSpaceAgentFailsFastOnFallbackPane(t *testing.T) {
	storeRoot := t.TempDir()
	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus:proj/box",
		HerdrWorkspaceID: "w-fallback-test",
		SandboxHandle:    "proj/box",
		GuestPaneID:      "pane-fallback",
	}
	if err := HerdrSpacePut(context.Background(), storeRoot, binding); err != nil {
		t.Fatal(err)
	}

	oldRead := herdrPaneReadFn
	herdrPaneReadFn = func(_ context.Context, _ string, _ string) (string, bool) {
		return guestShellFallbackMarker + " dial: connection refused", true
	}
	defer func() { herdrPaneReadFn = oldRead }()

	oldOpen := herdrOpenGuestShellPaneFn
	herdrOpenGuestShellPaneFn = func(_ context.Context, _, _, _, _ string, _ bool) (string, error) {
		return "pane-fresh", nil
	}
	defer func() { herdrOpenGuestShellPaneFn = oldOpen }()

	var buf strings.Builder
	_, err := herdrSpaceAgentCheckFallbackPane(
		context.Background(), "/bin/herdr", "proj/box", &binding, storeRoot, &buf,
	)
	if err == nil {
		t.Fatal("expected error when fresh pane is also a fallback; got nil")
	}
	if !strings.Contains(err.Error(), "fallback") {
		t.Errorf("error should mention fallback; got: %v", err)
	}
}

func TestSpaceAgentReplacesFallbackPane(t *testing.T) {
	storeRoot := t.TempDir()
	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus:proj/box",
		HerdrWorkspaceID: "w-replace-test",
		SandboxHandle:    "proj/box",
		GuestPaneID:      "pane-fallback",
	}
	if err := HerdrSpacePut(context.Background(), storeRoot, binding); err != nil {
		t.Fatal(err)
	}

	callCount := 0
	oldRead := herdrPaneReadFn
	herdrPaneReadFn = func(_ context.Context, _ string, paneID string) (string, bool) {
		callCount++
		if paneID == "pane-fallback" {
			return guestShellFallbackMarker + " dial: refused", true
		}
		return "root@sandbox:~$", true
	}
	defer func() { herdrPaneReadFn = oldRead }()

	openCalled := false
	oldOpen := herdrOpenGuestShellPaneFn
	herdrOpenGuestShellPaneFn = func(_ context.Context, _, _, _, _ string, _ bool) (string, error) {
		openCalled = true
		return "pane-fresh", nil
	}
	defer func() { herdrOpenGuestShellPaneFn = oldOpen }()

	var buf strings.Builder
	newPaneID, err := herdrSpaceAgentCheckFallbackPane(
		context.Background(), "/bin/herdr", "proj/box", &binding, storeRoot, &buf,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !openCalled {
		t.Error("expected herdrOpenGuestShellPaneFn to be called for fresh pane")
	}
	if newPaneID != "pane-fresh" {
		t.Errorf("paneID = %q, want pane-fresh", newPaneID)
	}
	if binding.GuestPaneID != "pane-fresh" {
		t.Errorf("binding.GuestPaneID = %q, want pane-fresh", binding.GuestPaneID)
	}
}
