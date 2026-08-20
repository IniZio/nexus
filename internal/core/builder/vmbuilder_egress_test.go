// Package builder_test — egress posture of the transient builder record.
//
// The builder VM's network policy is not decided in this process. In
// production the driver is the CLI's supervisorBuilderDriver, which boots the
// VM under a DETACHED supervisor; that supervisor reads the transient
// __builder record back from the store and builds the perimeter from its
// Envelope. The record is therefore the only channel through which BuildInVM
// can state what egress the builder needs.
package builder_test

import (
	"context"
	"sync"
	"testing"

	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// capturingBuilderStore keeps every record as it was CREATED. The real flow
// deletes the transient record on exit, so a store that only exposes live
// records cannot answer what the supervisor would have read.
type capturingBuilderStore struct {
	mu      sync.Mutex
	created []domain.Sandbox
}

var _ builder.BuilderStore = (*capturingBuilderStore)(nil)

func (c *capturingBuilderStore) Create(_ context.Context, sb domain.Sandbox) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.created = append(c.created, sb)
	return nil
}

func (c *capturingBuilderStore) Update(_ context.Context, _ domain.SandboxID, _ func(*domain.Sandbox) error) error {
	return nil
}

func (c *capturingBuilderStore) Delete(_ context.Context, _ domain.SandboxID) error { return nil }

func (c *capturingBuilderStore) first(t *testing.T) domain.Sandbox {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.created) == 0 {
		t.Fatal("no transient record was created — BuildInVM never reached the persist step")
	}
	return c.created[0]
}

// TestBuildInVM_TransientRecordOpensEgress pins the fix for a live regression:
// the transient record was created with a zero Envelope, so the detached
// supervisor built a DEFAULT-DENY perimeter for the builder VM. buildkitd
// could resolve registry-1.docker.io (DNS is answered by the netstack itself)
// but every TCP connection to it was refused, so no base image could ever be
// pulled and `nexus3 sandbox create --file` failed for every Containerfile
// with a FROM line.
//
// Since D-PD-33 an empty AllowedHosts no longer implies allow-all, so the
// builder must opt in EXPLICITLY. A zero Envelope here is the bug.
func TestBuildInVM_TransientRecordOpensEgress(t *testing.T) {
	st := &capturingBuilderStore{}
	seq := &seqCounter{}
	drv := newStopTracker(seq)
	et := newExecTracker(seq)

	spec := builder.BuilderVMSpec{
		RootfsDiskPath:   "/dev/null",
		ArtifactDiskPath: "", // fails later; the record is created first
	}
	// The build is expected to fail — we only care about the record it wrote
	// on the way through.
	_, _ = builder.BuildInVM(context.Background(), drv, spec, nil, et.fn, st)

	rec := st.first(t)
	if !rec.Envelope.OpenEgress {
		t.Error("transient builder record has OpenEgress=false: the detached supervisor " +
			"will build a default-deny perimeter and buildkitd cannot pull any base image")
	}
	if rec.Project != "__builder" {
		t.Errorf("transient record project = %q, want %q", rec.Project, "__builder")
	}
	if !rec.RemoveOnExit {
		t.Error("transient record must be marked RemoveOnExit so it cannot outlive the build")
	}
}

// TestBuildInVM_BuilderCarriesNoSecrets guards the other half of the posture:
// wildcard egress on the builder is only acceptable because the builder guest
// holds no credentials. If a future change seeds secrets onto this record,
// open egress stops being safe and this test should force that conversation.
func TestBuildInVM_BuilderCarriesNoSecrets(t *testing.T) {
	st := &capturingBuilderStore{}
	seq := &seqCounter{}
	drv := newStopTracker(seq)
	et := newExecTracker(seq)

	spec := builder.BuilderVMSpec{RootfsDiskPath: "/dev/null", ArtifactDiskPath: ""}
	_, _ = builder.BuildInVM(context.Background(), drv, spec, nil, et.fn, st)

	rec := st.first(t)
	if len(rec.Envelope.SecretHosts) != 0 {
		t.Errorf("builder record carries SecretHosts %v — open egress + secrets is not an acceptable posture",
			rec.Envelope.SecretHosts)
	}
	if len(rec.Envelope.SecretSpecs) != 0 {
		t.Error("builder record carries SecretSpecs — open egress + secrets is not an acceptable posture")
	}
}
