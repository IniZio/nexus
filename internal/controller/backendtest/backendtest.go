// Package backendtest provides an in-memory fake and a contract suite for
// controller.AgentBackend.
package backendtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/herdragent"
)

// Provision records a single Provision call for later assertion.
type Provision struct {
	Project   string
	Ref       controller.ThreadRef
	Principal string
	SandboxID string
	AgentRef  string
}

type agentState struct {
	promptText string
	script     []herdragent.State
	readAns    string
	tornDown   bool
}

// Fake is a goroutine-safe in-memory implementation of controller.AgentBackend.
type Fake struct {
	mu         sync.Mutex
	counter    atomic.Uint64
	provisions []Provision
	answers    []controller.AgentInput
	agents     map[string]*agentState
	sandboxes  map[string]string
}

// New returns a ready-to-use Fake.
func New() *Fake {
	return &Fake{
		agents:    make(map[string]*agentState),
		sandboxes: make(map[string]string),
	}
}

// Provisioned returns all recorded Provision calls (copy; safe to inspect from tests).
func (f *Fake) Provisioned() []Provision {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Provision, len(f.provisions))
	copy(out, f.provisions)
	return out
}

func (f *Fake) nextID() uint64 { return f.counter.Add(1) }

// Provision allocates deterministic ids "sb-N" / "agent-N" and records the call.
func (f *Fake) Provision(_ context.Context, project string, ref controller.ThreadRef, principal string) (string, string, error) {
	n := f.nextID()
	sbID := fmt.Sprintf("sb-%d", n)
	agRef := fmt.Sprintf("agent-%d", n)

	f.mu.Lock()
	f.agents[agRef] = &agentState{}
	f.sandboxes[sbID] = agRef
	f.provisions = append(f.provisions, Provision{
		Project:   project,
		Ref:       ref,
		Principal: principal,
		SandboxID: sbID,
		AgentRef:  agRef,
	})
	f.mu.Unlock()
	return sbID, agRef, nil
}

var (
	errUnknownAgent = errors.New("backendtest: unknown or torn-down agentRef")
	errAnswerInput  = errors.New("backendtest: exactly one of Text or Key must be set")
)

func (f *Fake) lookupAgent(agentRef string) (*agentState, error) {
	a, ok := f.agents[agentRef]
	if !ok || a.tornDown {
		return nil, fmt.Errorf("%w: %s", errUnknownAgent, agentRef)
	}
	return a, nil
}

func (f *Fake) Prompt(_ context.Context, agentRef, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.lookupAgent(agentRef)
	if err != nil {
		return "", err
	}
	a.promptText = text
	if len(a.script) == 0 {
		a.readAns = "echo: " + text
		a.script = []herdragent.State{{Status: herdragent.StatusDone, Settled: true}}
	}
	return "", nil
}

// Script queues states to be returned by successive Observe calls.
func (f *Fake) Script(agentRef string, states ...herdragent.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[agentRef]
	if !ok {
		return
	}
	a.script = append(a.script, states...)
}

// Observe returns the next queued state (last sticks once exhausted).
func (f *Fake) Observe(_ context.Context, agentRef string, _ bool) (herdragent.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.lookupAgent(agentRef)
	if err != nil {
		return herdragent.State{}, err
	}
	if len(a.script) == 0 {
		return herdragent.State{Status: herdragent.StatusIdle, Settled: true}, nil
	}
	st := a.script[0]
	if len(a.script) > 1 {
		a.script = a.script[1:]
	}
	return st, nil
}

func (f *Fake) Answer(_ context.Context, agentRef string, in controller.AgentInput) error {
	if (in.Text == "") == (in.Key == "") {
		return errAnswerInput
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.lookupAgent(agentRef); err != nil {
		return err
	}
	f.answers = append(f.answers, in)
	return nil
}

// Answered returns a copy of all AgentInput values passed to Answer.
func (f *Fake) Answered() []controller.AgentInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]controller.AgentInput, len(f.answers))
	copy(out, f.answers)
	return out
}

// ReadAnswer returns the canned answer set by Prompt; errors on unknown/torn-down agent.
func (f *Fake) ReadAnswer(_ context.Context, agentRef, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, err := f.lookupAgent(agentRef)
	if err != nil {
		return "", err
	}
	return a.readAns, nil
}

// Restart returns agentRef unchanged; errors on unknown/torn-down agent.
func (f *Fake) Restart(_ context.Context, _ string, agentRef string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.lookupAgent(agentRef); err != nil {
		return "", err
	}
	return agentRef, nil
}

// Teardown marks the agent torn down; unknown sandboxID returns nil (idempotent).
func (f *Fake) Teardown(_ context.Context, sandboxID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	agRef, ok := f.sandboxes[sandboxID]
	if !ok {
		return nil // unknown id: idempotent nil
	}
	if a, exists := f.agents[agRef]; exists {
		a.tornDown = true
	}
	return nil
}

var knownStatuses = map[herdragent.Status]bool{
	herdragent.StatusIdle:    true,
	herdragent.StatusWorking: true,
	herdragent.StatusBlocked: true,
	herdragent.StatusDone:    true,
	herdragent.StatusUnknown: true,
}

// RunBackendContract runs interface-level contract tests against any AgentBackend.
func RunBackendContract(t *testing.T, newBackend func(t *testing.T) controller.AgentBackend) {
	t.Helper()

	t.Run("Provision/distinct ids", func(t *testing.T) {
		t.Helper()
		b := newBackend(t)
		ref1 := controller.NewThreadRef("T1", "C1", "1.0")
		ref2 := controller.NewThreadRef("T1", "C1", "2.0")
		sb1, ag1, err := b.Provision(context.Background(), "proj", ref1, "u:alice")
		if err != nil {
			t.Fatalf("Provision #1 error: %v", err)
		}
		sb2, ag2, err := b.Provision(context.Background(), "proj", ref2, "u:bob")
		if err != nil {
			t.Fatalf("Provision #2 error: %v", err)
		}
		if sb1 == "" || ag1 == "" {
			t.Fatalf("Provision #1 returned empty ids: sb=%q ag=%q", sb1, ag1)
		}
		if sb2 == "" || ag2 == "" {
			t.Fatalf("Provision #2 returned empty ids: sb=%q ag=%q", sb2, ag2)
		}
		if sb1 == sb2 {
			t.Fatalf("sandboxIDs are not distinct: %q", sb1)
		}
		if ag1 == ag2 {
			t.Fatalf("agentRefs are not distinct: %q", ag1)
		}
	})

	t.Run("Prompt+Observe reaches terminal", func(t *testing.T) {
		t.Helper()
		b := newBackend(t)
		ref := controller.NewThreadRef("T1", "C1", "ts1")
		_, ag, err := b.Provision(context.Background(), "proj", ref, "u:alice")
		if err != nil {
			t.Fatalf("Provision: %v", err)
		}
		if _, err := b.Prompt(context.Background(), ag, "hello"); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var lastSt herdragent.State
		for {
			st, err := b.Observe(ctx, ag, true)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if !knownStatuses[st.Status] {
				t.Fatalf("Observe returned unknown status %q", st.Status)
			}
			lastSt = st
			if st.Status == herdragent.StatusDone || st.Status == herdragent.StatusBlocked {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("timed out waiting for done/blocked; last status %q", lastSt.Status)
			case <-time.After(50 * time.Millisecond):
			}
		}
	})

	t.Run("ReadAnswer after done", func(t *testing.T) {
		t.Helper()
		b := newBackend(t)
		ref := controller.NewThreadRef("T1", "C1", "ts2")
		_, ag, err := b.Provision(context.Background(), "proj", ref, "u:alice")
		if err != nil {
			t.Fatalf("Provision: %v", err)
		}
		if _, err := b.Prompt(context.Background(), ag, "hello"); err != nil {
			t.Fatalf("Prompt: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for {
			st, err := b.Observe(ctx, ag, true)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if st.Status == herdragent.StatusDone || st.Status == herdragent.StatusBlocked {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatalf("timed out before done/blocked")
			case <-time.After(50 * time.Millisecond):
			}
		}
		if _, err := b.ReadAnswer(context.Background(), ag, ""); err != nil {
			t.Fatalf("ReadAnswer after done: %v", err)
		}
	})

	t.Run("Answer/invalid input errors", func(t *testing.T) {
		t.Helper()
		b := newBackend(t)
		ref := controller.NewThreadRef("T1", "C1", "ts3")
		_, ag, err := b.Provision(context.Background(), "proj", ref, "u:alice")
		if err != nil {
			t.Fatalf("Provision: %v", err)
		}
		if err := b.Answer(context.Background(), ag, controller.AgentInput{}); err == nil {
			t.Fatal("Answer with empty input should error")
		}
		if err := b.Answer(context.Background(), ag, controller.AgentInput{Text: "hi", Key: "1"}); err == nil {
			t.Fatal("Answer with both Text and Key should error")
		}
	})

	t.Run("Unknown agentRef errors", func(t *testing.T) {
		t.Helper()
		b := newBackend(t)
		const bogus = "agent-does-not-exist"
		if _, err := b.Prompt(context.Background(), bogus, "hi"); err == nil {
			t.Fatal("Prompt on unknown agentRef should error")
		}
		if _, err := b.Observe(context.Background(), bogus, false); err == nil {
			t.Fatal("Observe on unknown agentRef should error")
		}
		if err := b.Answer(context.Background(), bogus, controller.AgentInput{Text: "x"}); err == nil {
			t.Fatal("Answer on unknown agentRef should error")
		}
		if _, err := b.ReadAnswer(context.Background(), bogus, ""); err == nil {
			t.Fatal("ReadAnswer on unknown agentRef should error")
		}
	})

	t.Run("Teardown/idempotent and blocks Prompt", func(t *testing.T) {
		t.Helper()
		b := newBackend(t)
		ref := controller.NewThreadRef("T1", "C1", "ts4")
		sb, ag, err := b.Provision(context.Background(), "proj", ref, "u:alice")
		if err != nil {
			t.Fatalf("Provision: %v", err)
		}
		if err := b.Teardown(context.Background(), sb); err != nil {
			t.Fatalf("Teardown: %v", err)
		}
		if err := b.Teardown(context.Background(), sb); err != nil {
			t.Fatalf("second Teardown should be nil: %v", err)
		}
		if err := b.Teardown(context.Background(), "sb-unknown"); err != nil {
			t.Fatalf("Teardown of unknown id should be nil: %v", err)
		}
		if _, err := b.Prompt(context.Background(), ag, "hi"); err == nil {
			t.Fatal("Prompt after Teardown should error")
		}
	})
}
