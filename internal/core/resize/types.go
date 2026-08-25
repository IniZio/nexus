// Package resize defines the contract every auto-resize slice consumes:
// the guest→host telemetry [Sample] type, the three driver capability
// interfaces ([MemoryResizer], [CPUResizer], [DiskResizer]), a
// [TelemetrySource] abstraction for the host-side poll path, the
// [TelemetryVsockPort] constant, and a [Bounds] config type.
//
// Dependency rule: this package imports NOTHING from
// internal/core/driver/..., cmd/nexus3-agent/..., internal/supervisor/...,
// or internal/core/service/... . The isolation is what prevents import cycles
// when the driver, the guest agent, and the supervisor all depend here.
//
// Design decisions ported from motive.md §"Design Half B":
//   - D-DC-10: transport is host→guest polling (not OLD's push) so no
//     host-side hybrid-vsock listener is required; DialGuest is already proven.
//   - D-DC-11: vsock port 3002, adjacent to the port-forward mux (3001).
//   - D-DC-12: governor is single-tenant (one sandbox per supervisor process);
//     OLD's workspaceID parameter is absent from every interface method.
package resize

import (
	"context"
	"time"
)

// TelemetryVsockPort is the vsock port the guest agent serves telemetry on.
//
// D-DC-11: 3002, adjacent to nexus3's existing port-forward mux port 3001
// (internal/core/service/forward_ops.go:13). Differs deliberately from
// OLD-nexus's 10799 (cmd/nexus-guest-agent/memory_stats.go:19) to avoid
// importing a foreign port-numbering convention.
const TelemetryVsockPort uint32 = 3002

// SampleMaxAge is the maximum age the governor accepts for a telemetry sample.
// Samples older than this are rejected to prevent a stale-sample resize
// cascade (e.g. after a VM suspend/resume where the clock jumped).
//
// Matches OLD-nexus memorySampleMaxAge / cpuSampleMaxAge (60s each,
// memory_resize.go:36, cpu_resize.go:28).
const SampleMaxAge = 60 * time.Second

// DiskSample is per-disk usage telemetry for one resizable disk, keyed by its
// 0-based index into the VM's ExtraDisks (same index space as GrowRequest.DiskIndex
// and Config.ResizableDiskIndices).
type DiskSample struct {
	Index      int    `json:"index"`
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
	Supported  bool   `json:"supported"`
}

// Sample is the telemetry payload the guest agent reports to the host
// governor on each poll. All three governor axes (memory, CPU, disk) are
// carried in a single struct so one vsock round-trip covers everything.
//
// Field names mirror OLD-nexus workspace.MemoryStatsSample
// (internal/core/workspace types), extended with disk and vCPU fields that
// nexus3 adds because it manages all three axes from a single governor.
//
// Disk fields (DiskUsed/TotalBytes) are populated from statfs of the
// workspace mount (AR-GA-AC1). OLD-nexus polled disk usage via DiskResizer
// (driver exec); nexus3 carries it here because the telemetry poll already
// visits the guest — a separate exec round-trip is unnecessary.
//
// VCPUOnline is the guest's view of currently online CPUs. The host governor
// could track this from its own resize calls, but a ground-truth field
// prevents governor state from drifting on partial CH failures.
type Sample struct {
	// Timestamp is the UTC wall time when the guest took the sample.
	// The governor rejects samples older than SampleMaxAge (e.g. after
	// VM suspend/resume where the clock jumped).
	Timestamp time.Time `json:"timestamp"`

	// MemAvailableBytes and MemTotalBytes are from /proc/meminfo.
	// MemAvailableBytes counts reclaimable page cache (like the free command),
	// so it stays flat under cache pressure and falls off a cliff just before
	// the OOM killer fires — making it a lagging-but-reliable backstop.
	MemAvailableBytes uint64 `json:"mem_available_bytes"`
	MemTotalBytes     uint64 `json:"mem_total_bytes"`

	// MemPSISomeAvg10 and MemPSIFullAvg10 are the 10-second some/full PSI
	// averages from /proc/pressure/memory (percent, 0–100). "some" stalls mean
	// at least one task is waiting for memory; "full" stalls mean all tasks are
	// waiting. The governor uses some_avg10 as the primary signal and full_avg10
	// as a critical-path bypass that skips the post-resize cooldown.
	//
	// MemPSISupported is false when CONFIG_PSI is absent or psi=0 is on the
	// cmdline. When false the governor falls back to the MemAvailable ratio
	// alone. nexus3's kernel has CONFIG_PSI=y with PSI_DEFAULT_DISABLED unset
	// (config-6.12.76:146-147), so this field will always be true in production.
	MemPSISomeAvg10 float64 `json:"mem_psi_some_avg10"`
	MemPSIFullAvg10 float64 `json:"mem_psi_full_avg10"`
	MemPSISupported bool    `json:"mem_psi_supported"`

	// CPUPSISomeAvg10 is the 10-second "some" PSI average from
	// /proc/pressure/cpu (percent, 0–100). /proc/pressure/cpu has only a
	// "some" line — there is no "full" for CPU pressure (unlike memory).
	//
	// CPUPSISupported is false when PSI is unavailable; the CPU governor
	// takes no action when this is false (neither grow nor shrink), as
	// documented by OLD-nexus cpu_resize_test.go TestCPUSampleSignals.
	CPUPSISomeAvg10 float64 `json:"cpu_psi_some_avg10"`
	CPUPSISupported bool    `json:"cpu_psi_supported"`

	// DiskUsedBytes and DiskTotalBytes are from statfs of the workspace mount.
	// The disk governor grows the backing image when
	// DiskUsedBytes/DiskTotalBytes > diskGrowThreshold (0.80).
	// The host derives the CH disk ID and guest device path from the sandbox's
	// ExtraDisks index — never from a hardcoded path such as /dev/vdb.
	//
	// DiskSupported is false when statfs failed or no workspace mount was found.
	// DiskTotalBytes == 0 is indistinguishable from "statfs failed" without this
	// flag — the same zero-reads-as-healthy trap that MemPSISupported and
	// CPUPSISupported exist to prevent. The disk governor must take no action
	// when DiskSupported is false.
	//
	// Deprecated in favour of DiskStats: these three fields mirror the
	// primary/workspace disk (DiskStats[0] when present) and are kept for
	// backward compatibility. Code that reads both sources should prefer DiskStats.
	DiskUsedBytes  uint64 `json:"disk_used_bytes"`
	DiskTotalBytes uint64 `json:"disk_total_bytes"`
	DiskSupported  bool   `json:"disk_supported"`

	// DiskStats carries per-disk usage telemetry for each resizable disk.
	// It is the generic successor to the single-disk DiskUsedBytes /
	// DiskTotalBytes / DiskSupported fields above.  Each entry's Index maps
	// to the same 0-based ExtraDisks index space used by GrowRequest.DiskIndex
	// and Config.ResizableDiskIndices.  The legacy fields above mirror the
	// primary/workspace disk entry for callers that have not yet migrated.
	// Omitted from JSON when the guest agent has not populated it (old agents).
	DiskStats []DiskSample `json:"disk_stats,omitempty"`

	// VCPUCount is the total number of vCPUs the VM was created with
	// (the MaxVCPUs ceiling set at vm.create). VCPUOnline is the number
	// currently online from the guest's perspective (after any hotplug events).
	// The governor uses VCPUOnline as a ground-truth check against its own
	// internal resize-call accounting to detect partial CH failures.
	VCPUCount  int32 `json:"vcpu_count"`
	VCPUOnline int32 `json:"vcpu_online"`
}

// Bounds carries the per-sandbox resource ceilings established at vm.create.
// They are fixed for the VM's lifetime; the governor uses them as clamp limits
// so it never requests more than the VM can accommodate.
//
// Mem and vCPU ceilings are set by AR-CLI; DiskMaxBytes is the hard ceiling
// for disk auto-grow (matches OLD-nexus diskMaxBytes = 100 GiB,
// internal/engine/workspace/disk_resize.go).
type Bounds struct {
	// MemMinBytes / MemMaxBytes are the allowed RAM range in bytes.
	// MemMaxBytes maps to the hotplug_size ceiling set at vm.create
	// (unverified assumption at AR0 time; gated on AR-SPIKE).
	MemMinBytes int64
	MemMaxBytes int64

	// VCPUMin / VCPUMax are the allowed vCPU count range.
	// VCPUMax maps to MaxVCPUs set at vm.create; CH cannot hotplug beyond it.
	VCPUMin int32
	VCPUMax int32

	// DiskMaxBytes is the hard ceiling for disk auto-grow in bytes.
	DiskMaxBytes int64
}

// MemoryResizer resizes guest RAM at runtime for backends that support it.
//
// Mirrors OLD internal/core/runtime/capabilities.go:31-39, adapted for
// nexus3's single-tenant governor (D-DC-12): no workspaceID parameter.
// The driver's ResizeMemory implementation calls CH PUT /api/v1/vm.resize
// with desired_ram (client.go:487).
type MemoryResizer interface {
	// ResizeMemory adjusts guest RAM to targetBytes and returns the new
	// current allocation. The implementation is responsible for clamping
	// targetBytes to the sandbox's [Bounds.MemMinBytes, Bounds.MemMaxBytes].
	ResizeMemory(ctx context.Context, targetBytes int64) (int64, error)
	// CurrentMemoryBytes returns the current runtime RAM allocation in bytes.
	CurrentMemoryBytes() int64
}

// CPUResizer hot-plugs or unplugs guest vCPUs at runtime.
//
// Mirrors OLD internal/core/runtime/capabilities.go:41-49, adapted for
// nexus3's single-tenant governor. The driver calls CH PUT /api/v1/vm.resize
// with desired_vcpus (client.go:487).
type CPUResizer interface {
	// ResizeCPU sets the desired online vCPU count and returns the new count.
	// The implementation clamps targetVCPUs to [Bounds.VCPUMin, Bounds.VCPUMax].
	ResizeCPU(ctx context.Context, targetVCPUs int32) (int32, error)
	// CurrentVCPUs returns the current desired/online vCPU count.
	CurrentVCPUs() int32
}

// DiskResizer grows the guest's workspace disk at runtime.
//
// nexus3 departs from OLD here in two ways: (1) it is single-tenant so there
// is no workspaceID, and (2) the interface takes a diskIndex rather than an
// implicit device path. This is necessary because nexus3 attaches N ExtraDisks
// and appends the workspace disk LAST (internal/core/service/create.go:383);
// the CH disk ID and guest device path must be derived from the index, not
// hardcoded. Half A already produced a /dev/vdb hardcode bug of exactly this
// shape (motive.md §HB — Gap 2).
//
// Disk usage for the grow decision comes from Sample.DiskUsed/TotalBytes,
// not from a DiskUsage method on this interface. OLD needed DiskUsage because
// it polled via driver exec; nexus3's telemetry poll already reaches the guest
// so disk stats ride the same round-trip for free.
type DiskResizer interface {
	// GrowDisk expands the host backing file for the disk at diskIndex in
	// ExtraDisks to targetBytes and notifies CH of the new size. diskIndex is
	// 0-based (ExtraDisks[0] → /dev/vdb, [1] → /dev/vdc, …). The guest runs
	// resize2fs separately via a [GrowRequest] wire command sent after GrowDisk
	// returns successfully.
	GrowDisk(ctx context.Context, diskIndex int, targetBytes int64) error
}

// TelemetrySource abstracts the host-side poll channel the governor calls to
// collect a [Sample] from the guest. Implementations dial vsock
// [TelemetryVsockPort] via the existing DialGuest path (D-DC-10).
//
// The interface is defined here (not in the driver) so the governor can be
// unit-tested with a fake without importing the driver package.
type TelemetrySource interface {
	// Poll opens a connection to the guest, sends a [SampleRequest], and
	// returns the decoded [Sample]. ctx carries the poll deadline.
	Poll(ctx context.Context) (Sample, error)
}
