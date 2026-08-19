// Package cloudhypervisor implements driver.Driver backed by Cloud Hypervisor
// (https://github.com/cloud-hypervisor/cloud-hypervisor). One CH process is
// spawned per sandbox and communicates with nexus3 via REST over a per-sandbox
// Unix socket (--api-socket).
//
// # Reentrancy prohibition
//
// This package MUST NOT import or call into internal/core/store or
// internal/core/service. Substrate methods are invoked while the caller holds
// the per-sandbox exclusive flock; re-entering the store deadlocks permanently.
package cloudhypervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"

	"github.com/IniZio/nexus3/internal/core/driver"
)

// CH v52 VmState strings — verified against:
//
//	vmm/src/api/openapi/cloud-hypervisor.yaml @ v52.0
//	VmState: { type: string, enum: [Created, Running, Shutdown, Paused] }
//
// Mapping rationale (nexus3's four-state model: Running / Paused / Absent / Unknown):
//
//	"Running"  → driver.Running   VM is executing.
//	"Paused"   → driver.Paused    VM memory preserved, execution suspended.
//	"Created"  → driver.Unknown   VM config loaded but guest not yet booted.
//	                               No nexus3 state cell for "present, not executing,
//	                               no memory to preserve." Running would make
//	                               recovery skip repair; Absent is destructive
//	                               (authorises a new Start onto an occupied socket).
//	                               Unknown triggers the conservative path in
//	                               recoverByID (|| obs.State == driver.Unknown guard).
//	"Shutdown" → driver.Unknown   Same reasoning: the VM object still lives in CH
//	                               memory and the socket is still bound. Returning
//	                               Absent here would authorise a second Start() that
//	                               collides with the existing VMM process. Stop()
//	                               calls vm.shutdown then vm.delete so this state
//	                               is transient (crash window between the two calls).
//	<other>    → driver.Unknown + error (fail loudly on unrecognised values)
func mapCHState(chState string) (driver.RunState, error) {
	switch chState {
	case "Running":
		return driver.Running, nil
	case "Paused":
		return driver.Paused, nil
	case "Created":
		return driver.Unknown,
			fmt.Errorf("cloudhypervisor: VM is in Created state (config loaded, guest not yet booted)")
	case "Shutdown":
		return driver.Unknown,
			fmt.Errorf("cloudhypervisor: VM is in Shutdown state (call Stop to fully delete)")
	default:
		return driver.Unknown,
			fmt.Errorf("cloudhypervisor: unrecognised VM state %q from cloud-hypervisor", chState)
	}
}

// isAbsent reports whether err means no VMM process is listening on the socket.
//
// ENOENT  — socket file does not exist (process never started or already gone).
// ECONNREFUSED — socket file exists but no listener (process exited, stale socket).
//
// Critically, every other error (context timeout, I/O error, TLS error, …)
// must NOT be treated as Absent: those mean we could not determine the state,
// which is driver.Unknown. Conflating Unknown with Absent is how a live VM
// gets destroyed.
func isAbsent(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// vmInfoResponse is the subset of Cloud Hypervisor's VmInfo we parse.
type vmInfoResponse struct {
	State string `json:"state"`
}

// chErrorResponse is CH's error body format: a JSON array where index 0 is
// "Error from API", index 1 is a human-readable description, and index 2 (when
// present) is the specific cause string.
//
// Verified against cloud-hypervisor v52.0 binary:
//
//	vm.info with no VM  → ["Error from API","The VM info is not available","VM is not created"]
//	vm.shutdown no VM   → ["Error from API","The VM could not shutdown","VM is not running"]
type chErrorResponse []string

// isNoVM returns true when CH reports that no VM has been created.
func (e chErrorResponse) isNoVM() bool {
	return len(e) >= 3 && e[2] == "VM is not created"
}

// isNotRunning returns true when CH reports that the VM is not running.
func (e chErrorResponse) isNotRunning() bool {
	return len(e) >= 3 && e[2] == "VM is not running"
}

// vmConfig is the subset of Cloud Hypervisor's VmConfig that nexus3 submits
// via PUT /api/v1/vm.create.
type vmConfig struct {
	Payload vmPayloadConfig `json:"payload"`
	CPUs    *vmCPUsConfig   `json:"cpus,omitempty"`
	Memory  *vmMemoryConfig `json:"memory,omitempty"`
	Serial  *vmSerialConfig `json:"serial,omitempty"`
	Disks   []vmDiskConfig  `json:"disks,omitempty"`
	Balloon *balloonConfig  `json:"balloon,omitempty"`
}

// vmDiskConfig maps to CH's DiskConfig for virtio-blk disk devices.
//
// image_type must be "raw" for raw ext4 images on CH v52+. Without it CH
// auto-detects the image type and disables sector-0 writes to protect
// against overwriting MBR/GPT partition tables; ext4 writes its superblock
// at sector 0 on rw mount, causing EIO (see CH device_manager.rs:2745
// "Disabling sector 0 writes").
//
// Verified against cloud-hypervisor.yaml @ v52.0 schema:
// DiskConfig { required: [path], properties: { path, image_type, readonly, direct, ... } }
// DiskImageType enum (CH v52): ["Raw", "Qcow2", "FixedVhd", "Vhdx", "Unknown"].
// direct (bool, default false): open the disk image with O_DIRECT so host page
// cache is bypassed. Use for scratch/data disks to prevent dirty writeback from
// exhausting host memory under high write load (e.g. buildkit layer export).
type vmDiskConfig struct {
	Path      string `json:"path"`
	ImageType string `json:"image_type,omitempty"`
	Direct    bool   `json:"direct,omitempty"`
}

// vmSerialConfig maps to CH's SerialConfig. When Mode is "File", File must be
// set to an absolute path; CH will create the file and write the guest's serial
// port output to it. Verified against cloud-hypervisor.yaml @ v52.0 schema:
// SerialConfig { required: [mode], properties: { file, socket, mode } }.
type vmSerialConfig struct {
	Mode string `json:"mode"`
	File string `json:"file,omitempty"`
}

type vmPayloadConfig struct {
	Kernel    string `json:"kernel,omitempty"`
	Cmdline   string `json:"cmdline,omitempty"`
	Initramfs string `json:"initramfs,omitempty"` // CH v52 PayloadConfig.initramfs — verified against cloud-hypervisor.yaml @ v52.0
}

// vmCPUsConfig maps to CH's CpusConfig; both boot_vcpus and max_vcpus are
// required by the CH schema.
//
// The Nested field maps to CpusConfig.nested (CH v53 OpenAPI schema,
// cloud-hypervisor.yaml @ v53.0: CpusConfig.nested boolean, default true).
// When true CH exposes the host CPU's virtualisation extensions (Intel VMX /
// AMD SVM) to the guest vCPUs, which allows the guest to run KVM itself.
//
// SECURITY: We always send this field explicitly (no omitempty). CH's schema
// default is true, so omitting it silently enables guest KVM — violating
// D-N3N-02's default-off guarantee. The zero value (false) is the correct
// default; Start sets it to true only when Config.NestedVirt opts in.
type vmCPUsConfig struct {
	BootVCPUs uint32 `json:"boot_vcpus"`
	MaxVCPUs  uint32 `json:"max_vcpus"`
	// Nested sets CpusConfig.nested in the vm.create request. Always sent
	// explicitly (no omitempty) so CH never falls back to its default (true).
	// false = default-off (D-N3N-02); true = NestedVirt opt-in path.
	Nested bool `json:"nested"`
}

// vmMemoryConfig maps to CH's MemoryConfig. The "size" field is in bytes.
//
// Verified against cloud-hypervisor.yaml @ v52.0:
// MemoryConfig { required: [size], properties: {
//
//	size (uint64), hotplug_size (uint64),
//	hotplug_method (string, enum: ["Acpi","VirtioMem"]),
//	shared (bool, default: false), hugepages (bool, default: false) } }
//
// hotplug_method defaults to "Acpi" in CH v52.0 and must be set explicitly to
// "VirtioMem" when a hotplug region is requested — leaving it implicit produces
// the wrong method silently (confirmed by auto-resize spike, Leg 1).
//
// shared enables POSIX shared memory (memfd) for the guest RAM region. CH
// REQUIRES this (or hugepages) for ANY vhost-user device including virtiofs —
// omitting it produces HTTP 500 "Using vhost-user requires using shared memory
// or huge pages". hugepages is NOT used: it requires host huge page
// pre-allocation and is a system-wide operator decision outside nexus3's scope.
type vmMemoryConfig struct {
	SizeBytes     uint64 `json:"size"`
	HotplugSize   uint64 `json:"hotplug_size,omitempty"`
	HotplugMethod string `json:"hotplug_method,omitempty"`
	// Shared backs guest RAM with a memfd shared-memory region. Required by
	// CH when any vhost-user device (virtiofs, vhost-user-net, …) is present.
	// Default false; omitempty omits it for sandboxes without live mounts.
	Shared bool `json:"shared,omitempty"`
}

// balloonConfig maps to CH's BalloonConfig for the virtio-balloon device.
//
// SizeBytes is the initial balloon size in bytes (0 = fully deflated; the
// balloon device still exists and free_page_reporting still works).
// DeflateOnOOM prevents the balloon from starving the guest under memory
// pressure — always set true so the guest is never killed by its own balloon.
// FreePageReporting enables passive free-page reclaim: the guest advertises
// pages it has freed back to the host without active inflation, allowing the
// host to reclaim memory the guest no longer uses.
//
// Verified against cloud-hypervisor.yaml @ v52.0:
// BalloonConfig { required: [size], properties: { size, deflate_on_oom, free_page_reporting } }
type balloonConfig struct {
	SizeBytes         uint64 `json:"size"`
	DeflateOnOOM      bool   `json:"deflate_on_oom,omitempty"`
	FreePageReporting bool   `json:"free_page_reporting,omitempty"`
}

// vmResizeRequest maps to CH's VmResize body for PUT /api/v1/vm.resize.
// All fields are optional; nil pointer fields are omitted from the JSON body.
//
// Verified against cloud-hypervisor.yaml @ v52.0:
// VmResize { properties: { desired_vcpus (uint32), desired_ram (uint64),
//
//	desired_balloon (uint64) } }
type vmResizeRequest struct {
	DesiredRAM     *uint64 `json:"desired_ram,omitempty"`
	DesiredVCPUs   *uint32 `json:"desired_vcpus,omitempty"`
	DesiredBalloon *uint64 `json:"desired_balloon,omitempty"`
}

// vmResizeDiskRequest maps to CH's VmResizeDisk body for PUT /api/v1/vm.resize-disk.
//
// Verified against cloud-hypervisor.yaml @ v52.0:
// VmResizeDisk { properties: { id (string), desired_size (int64) } }
// There is no "required:" list in the schema; CH enforces presence at runtime.
// The "desired_*" prefix follows the same convention as VmResize (desired_vcpus,
// desired_ram, desired_balloon) — which is why memory/vCPU resize works but disk
// resize silently failed when the old "size" field name was used.
//
// Ported from OLD packages/nexus/internal/vm/driver/cloudhypervisor/types.go:60-63.
type vmResizeDiskRequest struct {
	ID          string `json:"id"`
	DesiredSize uint64 `json:"desired_size"`
}

// client speaks the Cloud Hypervisor REST API over a Unix socket.
// Zero value is invalid; use newClient.
type client struct {
	socketPath string
	http       *http.Client
}

// newClient returns a client wired to socketPath. No connection is made until
// the first call.
func newClient(socketPath string) *client {
	return &client{
		socketPath: socketPath,
		http: &http.Client{
			Transport: &http.Transport{
				// Always dial the Unix socket, ignoring the URL host/port.
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
				// Disable keep-alives: a dead VMM must not be hidden by a
				// pooled connection that looks alive.
				DisableKeepAlives: true,
			},
		},
	}
}

// apiBase is the CH REST API root. The host is ignored (we always dial the
// Unix socket), but the URL must be syntactically valid.
const apiBase = "http://ch/api/v1"

// do executes one HTTP request and returns the raw response. The caller is
// responsible for draining and closing resp.Body. Connection errors from the
// underlying transport are returned unwrapped so callers can use isAbsent.
func (c *client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("cloudhypervisor: marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Return unwrapped so isAbsent can walk the error chain for
		// ENOENT / ECONNREFUSED. (fmt.Errorf with %w still preserves the
		// chain via errors.Is, but not wrapping keeps the message cleaner.)
		return nil, err
	}
	return resp, nil
}

// drainClose reads and discards the response body and closes it, preventing
// connection leaks.
func drainClose(r *http.Response) {
	if r != nil && r.Body != nil {
		_, _ = io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
}

// Ping sends GET /api/v1/vmm.ping to check whether the VMM process is alive.
// Returns nil on 200 OK. Connection errors are returned as-is so callers can
// use isAbsent.
func (c *client) Ping(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/vmm.ping", nil)
	if err != nil {
		return err
	}
	drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudhypervisor: vmm.ping: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// VMInfo queries GET /api/v1/vm.info and returns the mapped driver.RunState.
//
// Behaviour by outcome:
//   - Connection refused or socket missing      → returns error (caller uses isAbsent)
//   - HTTP 200 with recognised state            → (mapped state, nil or err per mapCHState)
//   - HTTP 200 with unrecognised state          → (driver.Unknown, non-nil error)
//   - HTTP 500 "VM is not created"              → (driver.Absent, nil) — determined: no VM
//   - Any other non-200 status or unrecognised
//     500 body                                  → (driver.Unknown, non-nil error)
//   - Malformed JSON                            → (driver.Unknown, non-nil error)
//
// Verified against cloud-hypervisor v52.0: vm.info returns 500 with cause
// "VM is not created" when no VM has been configured in the VMM.
//
// The 500 "VM is not created" → Absent mapping is safe because spawnVMM now
// pre-flights the socket before spawning: if a live VMM is already bound, it
// returns ErrVMMAlreadyBound rather than launching a colliding process. This
// makes Absent unambiguous — "no VM exists here" — whether the socket is
// unreachable (dead VMM) or reachable but empty (crashed between spawnVMM and
// vm.create).
func (c *client) VMInfo(ctx context.Context) (driver.RunState, error) {
	resp, err := c.do(ctx, http.MethodGet, "/vm.info", nil)
	if err != nil {
		return driver.Unknown, err
	}
	defer drainClose(resp)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusInternalServerError {
			var chErr chErrorResponse
			if json.Unmarshal(body, &chErr) == nil && chErr.isNoVM() {
				// CH confirmed: no VM has been created in this VMM. This is a
				// determined observation — return Absent with nil error.
				return driver.Absent, nil
			}
		}
		return driver.Unknown,
			fmt.Errorf("cloudhypervisor: vm.info: unexpected status %d: %s",
				resp.StatusCode, body)
	}

	var info vmInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return driver.Unknown,
			fmt.Errorf("cloudhypervisor: vm.info: decode response: %w", err)
	}

	return mapCHState(info.State)
}

// VMCreate sends PUT /api/v1/vm.create with cfg. Returns nil on 204.
//
// The response body is included in the error on non-204 status codes.
// Empirically verified: CH v52 accepts bad kernel/initramfs paths at create
// time (returns 204) and reports file-not-found errors at vm.boot time instead.
func (c *client) VMCreate(ctx context.Context, cfg vmConfig) error {
	resp, err := c.do(ctx, http.MethodPut, "/vm.create", cfg)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.create: %w", err)
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cloudhypervisor: vm.create: unexpected status %d: %s",
			resp.StatusCode, body)
	}
	return nil
}

// VMBoot sends PUT /api/v1/vm.boot. Returns nil on 204.
//
// The response body is included in the error on non-204 status codes.
// Empirically verified: CH v52 returns HTTP 500 with a JSON error array when
// the kernel file does not exist:
//
//	["Error from API","The VM could not boot","Cannot open kernel file","No such file or directory (os error 2)"]
func (c *client) VMBoot(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPut, "/vm.boot", nil)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.boot: %w", err)
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cloudhypervisor: vm.boot: unexpected status %d: %s",
			resp.StatusCode, body)
	}
	return nil
}

// VMPause sends PUT /api/v1/vm.pause. Returns nil on 204.
func (c *client) VMPause(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPut, "/vm.pause", nil)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.pause: %w", err)
	}
	drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("cloudhypervisor: vm.pause: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// VMResume sends PUT /api/v1/vm.resume. Returns nil on 204.
func (c *client) VMResume(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPut, "/vm.resume", nil)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.resume: %w", err)
	}
	drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("cloudhypervisor: vm.resume: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// VMShutdown sends PUT /api/v1/vm.shutdown, requesting a graceful guest
// power-off.
//
// Idempotence: CH returns 500 "VM is not running" when there is no VM to shut
// down (verified v52.0). 404 and 405 are also accepted defensively. All three
// indicate "nothing to do" rather than a genuine failure.
func (c *client) VMShutdown(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPut, "/vm.shutdown", nil)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.shutdown: %w", err)
	}
	defer drainClose(resp)

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusNotFound, http.StatusMethodNotAllowed:
		return nil
	case http.StatusInternalServerError:
		// CH returns 500 "VM is not running" when no VM is configured.
		// Parse to confirm it's the expected "nothing to do" case.
		body, _ := io.ReadAll(resp.Body)
		var chErr chErrorResponse
		if json.Unmarshal(body, &chErr) == nil && (chErr.isNotRunning() || chErr.isNoVM()) {
			return nil
		}
		return fmt.Errorf("cloudhypervisor: vm.shutdown: status 500: %s", body)
	default:
		return fmt.Errorf("cloudhypervisor: vm.shutdown: unexpected status %d", resp.StatusCode)
	}
}

// VMMShutdown sends PUT /api/v1/vmm.shutdown, requesting the VMM process to
// exit. This terminates the cloud-hypervisor process itself (as opposed to
// vm.shutdown which asks the guest OS to power off).
//
// Verified against cloud-hypervisor v52.0: vmm.shutdown returns 200 OK (not
// 204 No Content). Both are accepted defensively.
//
// Returns the raw error (unwrapped) so callers can use isAbsent.
func (c *client) VMMShutdown(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPut, "/vmm.shutdown", nil)
	if err != nil {
		return err
	}
	drainClose(resp)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	default:
		return fmt.Errorf("cloudhypervisor: vmm.shutdown: unexpected status %d", resp.StatusCode)
	}
}

// VMDelete sends PUT /api/v1/vm.delete. Returns nil on 204.
func (c *client) VMDelete(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodPut, "/vm.delete", nil)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.delete: %w", err)
	}
	drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("cloudhypervisor: vm.delete: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// VMResize sends PUT /api/v1/vm.resize to adjust guest RAM, vCPU count, or
// virtio-balloon size at runtime. Nil parameters are omitted from the request
// body so callers only supply the dimensions they want to change. CH returns
// 204 No Content on success.
//
// To inflate the balloon (reclaim guest-freed memory on the host), pass a
// non-nil desiredBalloonBytes with the new target size. To deflate (return
// memory to the guest), pass desiredBalloonBytes pointing to 0.
//
// Verified against cloud-hypervisor.yaml @ v52.0:
// PUT /api/v1/vm.resize → VmResize → 204 No Content.
func (c *client) VMResize(ctx context.Context, desiredRAMBytes *uint64, desiredVCPUs *uint32, desiredBalloonBytes *uint64) error {
	req := vmResizeRequest{
		DesiredRAM:     desiredRAMBytes,
		DesiredVCPUs:   desiredVCPUs,
		DesiredBalloon: desiredBalloonBytes,
	}
	resp, err := c.do(ctx, http.MethodPut, "/vm.resize", req)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.resize: %w", err)
	}
	drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("cloudhypervisor: vm.resize: unexpected status %d", resp.StatusCode)
	}
	return nil
}

// VMResizeDisk sends PUT /api/v1/vm.resize-disk to notify CH that a backing
// file has grown and it should re-read the device capacity. CH returns 204 No
// Content on success.
//
// id is the CH-assigned disk identifier (e.g. "_disk1"). newSizeBytes is the
// new total capacity in bytes; it must be ≥ the current size (grow-only).
// The caller is responsible for expanding the host backing file before this
// call and rolling back on failure.
//
// Verified against cloud-hypervisor.yaml @ v52.0:
// PUT /api/v1/vm.resize-disk → VmResizeDisk → 204 No Content.
//
// Ported from OLD packages/nexus/internal/vm/driver/cloudhypervisor/client.go:143-146.
func (c *client) VMResizeDisk(ctx context.Context, id string, newSizeBytes uint64) error {
	req := vmResizeDiskRequest{
		ID:          id,
		DesiredSize: newSizeBytes,
	}
	resp, err := c.do(ctx, http.MethodPut, "/vm.resize-disk", req)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.resize-disk: %w", err)
	}
	drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("cloudhypervisor: vm.resize-disk %s: unexpected status %d", id, resp.StatusCode)
	}
	return nil
}
