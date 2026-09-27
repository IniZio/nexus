//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	herdrbackend "github.com/IniZio/nexus/internal/controller/backend/herdr"
	"github.com/IniZio/nexus/internal/controller/chattest"
	"github.com/IniZio/nexus/internal/controller/sandbox"
	"github.com/IniZio/nexus/internal/controller/store/sqlite"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/connectors"
	"github.com/IniZio/nexus/internal/herdragent"
	"github.com/IniZio/nexus/internal/testutil/livenexus"
)

const (
	testTeam    = "T0TEST"
	testUser    = "U0TEST"
	testUser2   = "U0TEST2"
	testChannel = "C0TEST"
	testProject = "ctrl-e2e-test"
)

func TestControllerE2E(t *testing.T) {
	if os.Getenv("NEXUS_LIVE_E2E") == "" && os.Getenv("CI") == "" {
		t.Skip("set NEXUS_LIVE_E2E=1 to run live controller e2e")
	}

	h := livenexus.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	t.Logf("isolated state root: %s", h.StateRoot())
	t.Logf("vault key path: %s", h.VaultKeyPath())

	privateRepo := resolvePrivateRepo(t)
	repoPath := initMinimalRepo(t, privateRepo)
	v := openIsolatedVault(t, h)
	store := openSQLiteStore(t, h)
	chat := chattest.New()
	backend := buildBackend(t, h, repoPath)
	lc := buildLifecycle(h)
	linker := controller.NewVaultLinker(v, vault.NewRegistry(), chat, testTeam, "")
	projects := fixedProjectResolver{channel: testChannel, project: testProject}

	deps := controller.Deps{
		Chat:      chat,
		Store:     store,
		Backend:   backend,
		Lifecycle: lc,
		Linker:    linker,
		Projects:  projects,
		IdleFor: func(string) controller.IdleThresholds {
			return controller.IdleThresholds{Pause: e2eIdlePause, Stop: e2eIdleStop}
		},
	}

	ctrl := controller.New(deps)
	var clockOffsetNs atomic.Int64
	clockFn := func() time.Time { return time.Now().Add(time.Duration(clockOffsetNs.Load())) }
	advanceClock := func(d time.Duration) { clockOffsetNs.Add(int64(d)) }
	router := controller.NewRouter(store, ctrl, controller.WithClock(clockFn))
	defer router.Close()

	adapterCtx, adapterCancel := context.WithCancel(ctx)
	defer adapterCancel()
	go func() { _ = chat.Run(adapterCtx, router.Handle) }()

	var sandboxIDs []string
	trackedSet := map[string]bool{}
	trackSandbox := func(id string) {
		if id != "" && !trackedSet[id] {
			trackedSet[id] = true
			sandboxIDs = append(sandboxIDs, id)
		}
	}
	t.Cleanup(func() {
		// Snapshot logs BEFORE Teardown: service.Remove (called inside Teardown)
		// deletes supervisor state dirs, which removes supervisor.log.
		// SnapshotLogs must run first so per-workspace logs are preserved.
		h.SnapshotLogs()
		tearCtx, tearCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer tearCancel()
		for _, id := range sandboxIDs {
			if err := backend.Teardown(tearCtx, id); err != nil {
				t.Logf("teardown %s: %v", id, err)
			}
		}
	})

	t.Run("UnlinkedUserRefused", func(t *testing.T) {
		ref := controller.NewThreadRef(testTeam, testChannel, "ts-unlinked")
		testUnlinkedRefused(t, ctx, chat, router, ref)
	})

	token := resolveGHToken(t)
	seedVault(t, ctx, v, token)

	var sharedTask controller.Task

	t.Run("MentionProvisionsSandbox", func(t *testing.T) {
		ref := controller.NewThreadRef(testTeam, testChannel, "ts-provision")
		sharedTask = testMentionProvisions(t, ctx, chat, router, ref, store)
		trackSandbox(sharedTask.SandboxID)
	})

	t.Run("second_turn", func(t *testing.T) {
		if sharedTask.SandboxID == "" {
			t.Fatal("no sandbox from MentionProvisionsSandbox")
		}
		testSecondTurn(t, ctx, chat, router, store, sharedTask)
	})

	t.Run("other_user_refused", func(t *testing.T) {
		if sharedTask.SandboxID == "" {
			t.Fatal("no sandbox from MentionProvisionsSandbox")
		}
		seedVaultUser(t, ctx, v, token, testUser2)
		testOtherUserRefused(t, ctx, chat, router, store, sharedTask)
	})

	t.Run("IdleSweepPausesAndResumes", func(t *testing.T) {
		if sharedTask.SandboxID == "" {
			t.Fatal("no sandbox from MentionProvisionsSandbox")
		}
		testIdleSweep(t, ctx, router, store, h, sharedTask, advanceClock, e2eIdlePause)
	})

	t.Run("GuestGHTokenIsBrokerPlaceholder", func(t *testing.T) {
		if sharedTask.SandboxID == "" {
			t.Fatal("no sandbox from MentionProvisionsSandbox")
		}
		testGuestGHToken(t, ctx, h, token, privateRepo, sharedTask.SandboxID)
	})

	t.Run("AgentRunsInGuest", func(t *testing.T) {
		testAgentRunsInGuest(t, ctx, chat, router, store, h, trackSandbox)
	})

	t.Run("ApprovalDialogBlocked", func(t *testing.T) {
		ref := controller.NewThreadRef(testTeam, testChannel, "ts-blocked")
		testApprovalBlocked(t, ctx, chat, router, store, ref, h, trackSandbox)
	})

	t.Run("LinearMCPOptional", func(t *testing.T) {
		testLinearMCP(t, ctx, v)
	})
}

func resolveGHToken(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		t.Fatalf("gh auth token failed: %v", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		t.Fatal("gh auth token returned empty string")
	}
	return token
}

func testUnlinkedRefused(t *testing.T, ctx context.Context, chat *chattest.Fake, router *controller.Router, ref controller.ThreadRef) {
	t.Helper()
	ev := controller.Event{
		Kind:      controller.EventMention,
		ThreadRef: ref,
		User:      testUser,
		Text:      "hello",
	}
	if err := router.Handle(ctx, ev); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range chat.Reactions(ref) {
			if r == "warning" {
				t.Logf("warning reaction posted for unlinked user")
				return
			}
		}
		for _, p := range chat.Posts(ref) {
			if strings.Contains(strings.ToLower(p), "link") {
				t.Logf("link hint posted: %q", p)
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("no refusal signal after unlinked mention; posts: %v reactions: %v", chat.Posts(ref), chat.Reactions(ref))
}

func seedVault(t *testing.T, ctx context.Context, v vault.Vault, token string) {
	t.Helper()
	principal := vault.SlackPrincipal(testTeam, testUser)
	k := vault.Key{Principal: principal, Integration: "github"}
	rec := vault.Record{
		AccessToken:     token,
		Expiry:          time.Now().Add(24 * time.Hour),
		AllowedProjects: []string{"*"},
	}
	if err := v.Put(ctx, k, rec); err != nil {
		t.Fatalf("vault.Put: %v", err)
	}
	t.Logf("seeded vault for principal %s (token redacted)", principal)
}

func testMentionProvisions(t *testing.T, ctx context.Context, chat *chattest.Fake, router *controller.Router, ref controller.ThreadRef, store controller.TaskStore) controller.Task {
	t.Helper()
	ev := controller.Event{
		Kind:      controller.EventMention,
		ThreadRef: ref,
		User:      testUser,
		Text:      "echo hello from nexus e2e test",
	}
	if err := router.Handle(ctx, ev); err != nil {
		t.Fatalf("Handle mention: %v", err)
	}

	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		reactions := chat.Reactions(ref)
		posts := chat.Posts(ref)
		for _, r := range reactions {
			if r == "white_check_mark" {
				task, _ := store.Get(ctx, ref)
				t.Logf("got check_mark; sandbox=%s status=%s", task.SandboxID, task.Status)
				echoExpected := "hello from nexus e2e test"
				found := false
				for _, p := range posts {
					if strings.Contains(p, echoExpected) {
						found = true
						t.Logf("echo output confirmed in agent response")
						break
					}
				}
				if !found {
					t.Errorf("MentionProvisionsSandbox: agent response missing %q; posts: %v", echoExpected, posts)
				}
				return task
			}
			if r == "warning" {
				t.Fatalf("provision failed (warning reaction); posts=%v", posts)
			}
		}
		for _, p := range posts {
			if strings.HasPrefix(p, "provision error:") {
				t.Fatalf("provision error: %s", p)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("no check_mark within 5 min; posts=%v reactions=%v", chat.Posts(ref), chat.Reactions(ref))
	return controller.Task{}
}

func testApprovalBlocked(t *testing.T, ctx context.Context, chat *chattest.Fake, router *controller.Router, store controller.TaskStore, ref controller.ThreadRef, h *livenexus.Harness, onSandbox func(string)) {
	t.Helper()
	probeFile := fmt.Sprintf("/tmp/nexus-approval-probe-%d", time.Now().UnixNano())
	ev := controller.Event{
		Kind:      controller.EventMention,
		ThreadRef: ref,
		User:      testUser,
		Text:      "Use the Bash tool to run exactly: touch " + probeFile + ". Do nothing else.",
	}
	if err := router.Handle(ctx, ev); err != nil {
		t.Fatalf("Handle mention: %v", err)
	}

	deadline := time.Now().Add(90 * time.Second)
	var task controller.Task
	for time.Now().Before(deadline) {
		var err error
		task, err = store.Get(ctx, ref)
		if err != nil && !errors.Is(err, controller.ErrNotFound) {
			t.Fatalf("store.Get: %v", err)
		}
		if err == nil {
			onSandbox(task.SandboxID)
		}
		if err == nil && task.Status == controller.StatusWaitingOnUser {
			t.Logf("agent blocked; posts: %v", chat.Posts(ref))
			break
		}
		for _, r := range chat.Reactions(ref) {
			if r == "warning" {
				t.Fatalf("testApprovalBlocked: provision failed (warning reaction); posts=%v", chat.Posts(ref))
			}
		}
		for _, p := range chat.Posts(ref) {
			if strings.HasPrefix(p, "provision error:") {
				t.Fatalf("testApprovalBlocked: %s", p)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	if task.Status != controller.StatusWaitingOnUser {
		t.Fatalf("agent did not reach waiting_on_user within 90s; approval dialog may not have triggered")
	}

	reply := controller.Event{
		Kind:      controller.EventReply,
		ThreadRef: ref,
		User:      testUser,
		Text:      "1",
	}
	if err := router.Handle(ctx, reply); err != nil {
		t.Fatalf("Handle reply: %v", err)
	}
	t.Logf("sent digit reply to unblock")

	deadline2 := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline2) {
		for _, r := range chat.Reactions(ref) {
			if r == "white_check_mark" {
				t.Logf("unblocked and completed")
				cur, _ := store.Get(ctx, ref)
				if cur.SandboxID != "" {
					if out, err := h.Run(ctx, "exec", cur.SandboxID, "--", "test", "-f", probeFile); err != nil {
						t.Errorf("approval probe %s not found in guest sandbox %s: %v\n%s", probeFile, cur.SandboxID, err, out)
					} else {
						t.Logf("approval probe confirmed in guest: %s", probeFile)
					}
				}
				if _, err := os.Stat(probeFile); err == nil {
					t.Errorf("approval probe file %s found on HOST — agent ran on host instead of guest", probeFile)
				} else {
					t.Logf("approval probe correctly absent on host: %s", probeFile)
				}
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Errorf("approval unblock reached timeout without check_mark")
}

func testIdleSweep(t *testing.T, ctx context.Context, router *controller.Router, store controller.TaskStore, h *livenexus.Harness, task controller.Task, advanceClock func(time.Duration), idlePause time.Duration) {
	t.Helper()
	ref := task.ThreadRef

	idleDeadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(idleDeadline) {
		cur, err := store.Get(ctx, ref)
		if err == nil && cur.Status == controller.StatusIdle {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	cur, getErr := store.Get(ctx, ref)
	if getErr != nil || cur.Status != controller.StatusIdle {
		t.Fatalf("task did not reach StatusIdle within 60s; status=%v err=%v", cur.Status, getErr)
	}

	advanceClock(idlePause * 2)
	fakeNow := time.Now().Add(idlePause * 2)
	idleBefore := fakeNow.Add(-idlePause)
	if err := router.Tick(ctx, idleBefore); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	paused := false
	for time.Now().Before(deadline) {
		cur, err := store.Get(ctx, ref)
		if err == nil && cur.Status == controller.StatusPaused {
			paused = true
			t.Logf("sandbox paused by idle sweep")
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !paused {
		t.Fatal("sandbox did not pause within 30s after idle sweep")
	}

	reply := controller.Event{
		Kind:      controller.EventReply,
		ThreadRef: ref,
		User:      testUser,
		Text:      "resumed by reply",
	}
	if err := router.Handle(ctx, reply); err != nil {
		t.Fatalf("Handle resume reply: %v", err)
	}

	deadline2 := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline2) {
		cur, err := store.Get(ctx, ref)
		if err == nil && (cur.Status == controller.StatusWorking || cur.Status == controller.StatusIdle || cur.Status == controller.StatusClosed) {
			t.Logf("sandbox resumed: status=%s", cur.Status)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Errorf("sandbox did not resume after reply")
}

func testGuestGHToken(t *testing.T, ctx context.Context, h *livenexus.Harness, hostToken, privateRepo, sbID string) {
	t.Helper()

	repoURL := "https://github.com/" + privateRepo

	// Assert GH_TOKEN placeholder is present in the guest before trying git.
	// The cred.env write and the VM resume are synchronous (supervisor seeds
	// before writing READY; CH VMResume completes before StatusWorking is set),
	// so a single check is sufficient — no retry loop needed.
	tokenCheckOut, _ := h.Run(ctx, "exec", sbID, "--", "sh", "-c", `printf '%s' "$GH_TOKEN"`)
	if strings.TrimSpace(tokenCheckOut) == "" {
		t.Fatal("identity check: GH_TOKEN is empty in guest — broker placeholder not injected")
	}

	// Use a credential helper so the token is passed as Basic auth (username +
	// password), which the broker intercepts. This avoids the http.extraHeader
	// approach which does not go through the credential broker path.
	credHelper := `'!f(){ echo username=x-access-token; echo "password=$GH_TOKEN"; }; f'`
	posOut, posErr := h.Run(ctx, "exec", sbID, "--", "sh", "-c",
		`git -c credential.helper= -c credential.helper=`+credHelper+` ls-remote `+repoURL+` HEAD`)
	if posErr != nil {
		t.Fatalf("identity check: git ls-remote with GH_TOKEN placeholder failed (broker substitution broken): %v\n%s", posErr, posOut)
	}
	t.Logf("identity verified: git ls-remote succeeded via broker for %s", privateRepo)

	bogusHelper := `'!f(){ echo username=x-access-token; echo "password=bogus-invalid-token"; }; f'`
	negOut, negErr := h.Run(ctx, "exec", sbID, "--", "sh", "-c",
		`git -c credential.helper= -c credential.helper=`+bogusHelper+` ls-remote `+repoURL+` HEAD`)
	if negErr == nil {
		t.Errorf("identity check: git ls-remote with bogus token should have failed\n%s", negOut)
	} else {
		t.Logf("negative control passed: bogus token correctly rejected")
	}

	ghTokenOut, _ := h.Run(ctx, "exec", sbID, "--", "sh", "-c", "echo $GH_TOKEN")
	ghToken := strings.TrimSpace(ghTokenOut)
	if ghToken == hostToken {
		t.Error("GH_TOKEN in guest must not be the raw host token (zero-cred-in-guest invariant)")
	}
	t.Logf("GH_TOKEN in guest differs from raw host token: broker placeholder in use")
}

func testAgentRunsInGuest(t *testing.T, ctx context.Context, chat *chattest.Fake, router *controller.Router, store controller.TaskStore, h *livenexus.Harness, onSandbox func(string)) {
	t.Helper()
	ref := controller.NewThreadRef(testTeam, testChannel, "ts-agent-in-guest")
	ev := controller.Event{
		Kind:      controller.EventMention,
		ThreadRef: ref,
		User:      testUser,
		Text:      "Use the Bash tool to run exactly: hostname",
	}
	if err := router.Handle(ctx, ev); err != nil {
		t.Fatalf("Handle mention: %v", err)
	}

	var sbID string
	deadline := time.Now().Add(5 * time.Minute)
outer:
	for time.Now().Before(deadline) {
		reactions := chat.Reactions(ref)
		posts := chat.Posts(ref)
		for _, r := range reactions {
			if r == "white_check_mark" {
				task, _ := store.Get(ctx, ref)
				sbID = task.SandboxID
				break outer
			}
			if r == "warning" {
				t.Fatalf("AgentRunsInGuest provision failed; posts=%v", posts)
			}
		}
		for _, p := range posts {
			if strings.HasPrefix(p, "provision error:") {
				t.Fatalf("AgentRunsInGuest provision error: %s", p)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	if sbID == "" {
		t.Fatalf("AgentRunsInGuest: no check_mark within 5 min; posts=%v reactions=%v", chat.Posts(ref), chat.Reactions(ref))
	}
	onSandbox(sbID)

	guestHostOut, err := h.Run(ctx, "exec", sbID, "--", "hostname")
	if err != nil {
		t.Fatalf("nexus exec hostname in guest: %v", err)
	}
	guestHostname := strings.TrimSpace(guestHostOut)
	if !strings.HasPrefix(guestHostname, "nexus-") {
		t.Fatalf("guest hostname %q does not have expected nexus- prefix", guestHostname)
	}
	t.Logf("AgentRunsInGuest: guest hostname=%s", guestHostname)

	posts := chat.Posts(ref)
	found := false
	for _, p := range posts {
		if strings.Contains(p, guestHostname) {
			found = true
			t.Logf("AgentRunsInGuest: guest hostname %q found in agent response", guestHostname)
			break
		}
	}
	if !found {
		t.Errorf("AgentRunsInGuest: guest hostname %q not found in agent posts: %v", guestHostname, posts)
	}

	hostHostname, _ := os.Hostname()
	if hostHostname != guestHostname {
		for _, p := range posts {
			if strings.Contains(p, hostHostname) {
				t.Errorf("AgentRunsInGuest: host hostname %q found in response — agent may have run on host", hostHostname)
			}
		}
	}
}

func testLinearMCP(t *testing.T, ctx context.Context, v vault.Vault) {
	t.Helper()
	home, _ := os.UserHomeDir()
	credPath := filepath.Join(home, ".claude", ".credentials.json")

	entry, err := readLinearMCPEntry(credPath)
	if err != nil || entry == nil {
		t.Skipf("no mcpOAuth entry for mcp.linear.app in %s; skipping Linear MCP sub-step", credPath)
	}

	if importErr := importMCPOAuthIntoVault(ctx, v, entry); importErr != nil {
		t.Fatalf("ImportMCPOAuthIntoVault: %v", importErr)
	}
	t.Logf("imported Linear MCP OAuth into isolated vault")

	linearKey := vault.Key{Principal: "local:nexus-e2e", Integration: "linear"}
	rec, getErr := v.Get(ctx, linearKey)
	if getErr != nil {
		t.Fatalf("vault.Get linear after import: %v", getErr)
	}
	if rec.AccessToken == "" {
		t.Error("imported Linear record has empty access token")
	}
	t.Logf("Linear MCP record present in isolated vault (token redacted)")
}

type mcpOAuthEntry struct {
	ServerName   string
	ServerURL    string
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
	ClientID     string
}

func readLinearMCPEntry(credPath string) (*mcpOAuthEntry, error) {
	data, err := os.ReadFile(credPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var raw struct {
		MCPOAuth map[string]struct {
			ServerName   string `json:"serverName"`
			ServerURL    string `json:"serverUrl"`
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    int64  `json:"expiresAt"`
			ClientID     string `json:"clientId"`
		} `json:"mcpOAuth"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	for _, e := range raw.MCPOAuth {
		if (strings.Contains(e.ServerURL, "mcp.linear.app") || strings.Contains(e.ServerName, "linear")) && e.AccessToken != "" {
			return &mcpOAuthEntry{
				ServerName:   e.ServerName,
				ServerURL:    e.ServerURL,
				AccessToken:  e.AccessToken,
				RefreshToken: e.RefreshToken,
				ExpiresAt:    e.ExpiresAt,
				ClientID:     e.ClientID,
			}, nil
		}
	}
	return nil, nil
}

func importMCPOAuthIntoVault(ctx context.Context, v vault.Vault, entry *mcpOAuthEntry) error {
	k := vault.Key{Principal: "local:nexus-e2e", Integration: "linear"}
	rec := vault.Record{
		AccessToken:     entry.AccessToken,
		RefreshToken:    entry.RefreshToken,
		AllowedProjects: []string{"*"},
	}
	if entry.ExpiresAt > 0 {
		rec.Expiry = time.UnixMilli(entry.ExpiresAt)
	}
	return v.Put(ctx, k, rec)
}

func seedVaultUser(t *testing.T, ctx context.Context, v vault.Vault, token, user string) {
	t.Helper()
	principal := vault.SlackPrincipal(testTeam, user)
	k := vault.Key{Principal: principal, Integration: "github"}
	rec := vault.Record{
		AccessToken:     token,
		Expiry:          time.Now().Add(24 * time.Hour),
		AllowedProjects: []string{"*"},
	}
	if err := v.Put(ctx, k, rec); err != nil {
		t.Fatalf("vault.Put user=%s: %v", user, err)
	}
	t.Logf("seeded vault for %s", principal)
}

func testSecondTurn(t *testing.T, ctx context.Context, chat *chattest.Fake, router *controller.Router, store controller.TaskStore, task controller.Task) {
	t.Helper()
	ref := task.ThreadRef

	// Confirm task is idle; testMentionProvisions waits for white_check_mark which
	// is posted after the StatusIdle transition, so this should be fast.
	idleDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(idleDeadline) {
		cur, err := store.Get(ctx, ref)
		if err == nil && cur.Status == controller.StatusIdle {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	cur, getErr := store.Get(ctx, ref)
	if getErr != nil || cur.Status != controller.StatusIdle {
		t.Fatalf("second_turn: task not idle; status=%v err=%v", cur.Status, getErr)
	}

	preCheckmarks := 0
	for _, r := range chat.Reactions(ref) {
		if r == "white_check_mark" {
			preCheckmarks++
		}
	}
	prePosts := len(chat.Posts(ref))

	ev := controller.Event{
		Kind:      controller.EventReply,
		ThreadRef: ref,
		User:      testUser,
		Text:      "echo nexus-second-turn-confirmed",
	}
	if err := router.Handle(ctx, ev); err != nil {
		t.Fatalf("second_turn: Handle: %v", err)
	}

	const wantPhrase = "nexus-second-turn-confirmed"
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		checkmarks := 0
		for _, r := range chat.Reactions(ref) {
			if r == "white_check_mark" {
				checkmarks++
			}
		}
		if checkmarks > preCheckmarks {
			posts := chat.Posts(ref)
			newPosts := posts[min(prePosts, len(posts)):]
			found := false
			for _, p := range newPosts {
				if strings.Contains(p, wantPhrase) {
					found = true
					break
				}
			}
			cur2, _ := store.Get(ctx, ref)
			if cur2.SandboxID != task.SandboxID {
				t.Errorf("second_turn: SandboxID changed: was %s, now %s", task.SandboxID, cur2.SandboxID)
			}
			if !found {
				t.Errorf("second_turn: check_mark posted but phrase %q absent in new posts: %v", wantPhrase, newPosts)
			}
			t.Logf("second_turn: complete; sandbox=%s status=%s", cur2.SandboxID, cur2.Status)
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("second_turn: no completion within 5 min; posts=%v reactions=%v", chat.Posts(ref), chat.Reactions(ref))
}

func testOtherUserRefused(t *testing.T, ctx context.Context, chat *chattest.Fake, router *controller.Router, store controller.TaskStore, task controller.Task) {
	t.Helper()
	ref := task.ThreadRef

	// Wait for idle so the owner check is reached (not the working/busy branch).
	idleDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(idleDeadline) {
		cur, err := store.Get(ctx, ref)
		if err == nil && cur.Status == controller.StatusIdle {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	cur, getErr := store.Get(ctx, ref)
	if getErr != nil || cur.Status != controller.StatusIdle {
		t.Fatalf("other_user_refused: task not idle; status=%v err=%v", cur.Status, getErr)
	}
	preSeq := cur.StateChangeSeq
	prePosts := len(chat.Posts(ref))

	ev := controller.Event{
		Kind:      controller.EventReply,
		ThreadRef: ref,
		User:      testUser2,
		Text:      "this should be refused",
	}
	if err := router.Handle(ctx, ev); err != nil {
		t.Fatalf("other_user_refused: Handle: %v", err)
	}

	// blocked.go posts "only <@owner> can drive this thread".
	ownerMention := "<@" + testUser + ">"
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		posts := chat.Posts(ref)
		for _, p := range posts[min(prePosts, len(posts)):] {
			if strings.Contains(p, ownerMention) {
				t.Logf("other_user_refused: refusal posted %q", p)
				cur2, _ := store.Get(ctx, ref)
				if cur2.StateChangeSeq != preSeq {
					t.Errorf("other_user_refused: StateChangeSeq changed (agent prompted): was %d, now %d", preSeq, cur2.StateChangeSeq)
				}
				t.Logf("other_user_refused: seq stable at %d; owner check passed", cur2.StateChangeSeq)
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Errorf("other_user_refused: no refusal post containing %q within 15s; posts=%v", ownerMention, chat.Posts(ref))
}

func openIsolatedVault(t *testing.T, h *livenexus.Harness) vault.Vault {
	t.Helper()
	keyData, err := os.ReadFile(h.VaultKeyPath())
	if err != nil {
		t.Fatalf("read vault key: %v", err)
	}
	vaultDir := filepath.Join(h.DataHome(), "nexus", "vault")
	st, err := vault.NewFileStore(vaultDir, keyData[:32])
	if err != nil {
		t.Fatalf("vault.NewFileStore: %v", err)
	}
	reg := vault.NewRegistry()
	_ = reg.Register(connectors.NewGitHub(""))
	return vault.NewVaultImpl(st, reg)
}

func openSQLiteStore(t *testing.T, h *livenexus.Harness) *sqlite.Store {
	t.Helper()
	dbDir := filepath.Join(h.StateRoot(), "nexus", "controller")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatalf("mkdir controller db: %v", err)
	}
	st, err := sqlite.Open(filepath.Join(dbDir, "tasks.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func buildBackend(t *testing.T, h *livenexus.Harness, repoPath string) *herdrbackend.Backend {
	t.Helper()
	cfg := herdrbackend.Config{
		RepoPath:        repoPath,
		Model:           "claude-haiku-4-5",
		PermissionMode:  "default",
		HerdrSocketPath: h.SocketPath(),
		NexusBin:        h.NexusBin(),
		ExtraEnv:        h.ExtraEnv(),
		WorktreeDir:     h.WorktreeDir(),
	}
	b := herdrbackend.New(cfg)
	b.SetAgentOpts(herdragent.WithSettle(200*time.Millisecond), herdragent.WithUnknownWindow(30*time.Second))
	return b
}

const (
	e2eIdlePause = 15 * time.Second
	e2eIdleStop  = 60 * time.Second
)

type cliLifecycle struct {
	h *livenexus.Harness
}

func buildLifecycle(h *livenexus.Harness) controller.SandboxLifecycle {
	return &cliLifecycle{h: h}
}

func (l *cliLifecycle) Pause(ctx context.Context, sandboxID string) error {
	out, err := l.h.Run(ctx, "herdr", "pause", sandboxID)
	if err != nil {
		return fmt.Errorf("nexus herdr pause %s: %w\n%s", sandboxID, err, out)
	}
	return nil
}

func (l *cliLifecycle) Resume(ctx context.Context, sandboxID string) error {
	out, err := l.h.Run(ctx, "herdr", "resume", sandboxID)
	if err != nil {
		return fmt.Errorf("nexus herdr resume %s: %w\n%s", sandboxID, err, out)
	}
	return nil
}

func (l *cliLifecycle) Stop(ctx context.Context, sandboxID string) error {
	out, err := l.h.Run(ctx, "sandbox", "stop", sandboxID)
	if err != nil {
		return fmt.Errorf("nexus sandbox stop %s: %w\n%s", sandboxID, err, out)
	}
	return nil
}

func (l *cliLifecycle) Start(ctx context.Context, sandboxID string) error {
	out, err := l.h.Run(ctx, "sandbox", "start", sandboxID)
	if err != nil {
		return fmt.Errorf("nexus sandbox start %s: %w\n%s", sandboxID, err, out)
	}
	return nil
}

type fixedProjectResolver struct {
	channel string
	project string
}

func (r fixedProjectResolver) Resolve(_ context.Context, channel string) (string, error) {
	if channel == r.channel {
		return r.project, nil
	}
	return "", controller.ErrNoProject
}

func resolvePrivateRepo(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("gh", "repo", "list", "--visibility", "private", "--limit", "1",
		"--json", "nameWithOwner", "-q", ".[0].nameWithOwner").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Skip("no private GitHub repo available; ensure gh auth is configured and account has private repos")
	}
	return strings.TrimSpace(string(out))
}

func initMinimalRepo(t *testing.T, privateRepo string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "nexus-e2e-repo-")
	if err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	for _, args := range [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "e2e@nexus"},
		{"git", "-C", dir, "config", "user.name", "nexus-e2e"},
	} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("nexus e2e test repo\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	nexusDir := filepath.Join(dir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0o755); err != nil {
		t.Fatalf("mkdir .nexus: %v", err)
	}
	repoPaths := "/" + privateRepo + "/**"
	nexusCfg := fmt.Sprintf(`version: 1
egress:
  secrets:
    - env: GH_TOKEN
      hosts:
        - github.com
        - api.github.com
        - uploads.github.com
  policy:
    - host: github.com
      paths:
        - %s
    - host: api.github.com
      paths:
        - /**
    - host: uploads.github.com
      paths:
        - /**
`, repoPaths)
	if err := os.WriteFile(filepath.Join(nexusDir, "config.yaml"), []byte(nexusCfg), 0o644); err != nil {
		t.Fatalf("write .nexus/config.yaml: %v", err)
	}
	containerfile := "FROM ghcr.io/inizio/nexus-base:latest\n"
	if err := os.WriteFile(filepath.Join(nexusDir, "Containerfile"), []byte(containerfile), 0o644); err != nil {
		t.Fatalf("write .nexus/Containerfile: %v", err)
	}

	for _, args := range [][]string{
		{"git", "-C", dir, "add", "."},
		{"git", "-C", dir, "commit", "-m", "init"},
	} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
	}
	return dir
}

var _ controller.SandboxLifecycle = (*cliLifecycle)(nil)
var _ = sandbox.NewServiceLifecycle
