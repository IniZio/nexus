package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ErrAgentUnreachable is returned by CreateAndBoot when the VM starts but the
// guest agent does not become reachable within the configured timeout.
var ErrAgentUnreachable = errors.New("service: guest agent did not answer after VM boot")

// DriverFactory constructs a Driver pre-configured for a specific ext4 disk
// image. CreateAndBoot calls it with the resolved ext4 path so that each
// sandbox boot uses a fresh driver instance with the correct DiskImagePath.
// The returned Driver is owned by CreateAndBoot and is not retained by svc.
type DriverFactory func(ext4Path string) (driver.Driver, error)

// ProbeFunc verifies that the guest agent inside the newly-booted VM is
// reachable and ready. It is called with a context that already carries the
// ReachabilityTimeout deadline. An error return causes CreateAndBoot to stop
// the VM, delete the record, and return ErrAgentUnreachable.
//
// The production implementation makes a lightweight connection attempt via
// driver.GuestDialer. Tests inject a stub (returning nil = reachable, error
// = unreachable) so no real VM or vsock is needed.
type ProbeFunc func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error

// ImageSpec describes how to locate a bootable ext4 artifact. Exactly one
// field should be set; CreateAndBoot returns an error if all are empty.
type ImageSpec struct {
	// Digest is a canonical "sha256:<hex>" image digest. The artifact is read
	// from the cache at <cacheRoot>/<algo>/<hex>/artifact.
	Digest string

	// Ref is a human-readable image tag (e.g. "nexus3-base:20260807"). The
	// image cache is scanned to find the matching entry. Ref may also be a
	// "sha256:<hex>" digest string — ParseDigest is tried first.
	Ref string

	// RootfsPath is a direct path to a raw ext4 file. No cache lookup is
	// performed. Intended as a dev convenience (--rootfs).
	RootfsPath string
}

// CreateAndBootOptions carries the options for CreateAndBoot.
type CreateAndBootOptions struct {
	// RemoveOnExit records the --rm intent durably at creation time.
	RemoveOnExit bool

	// Image describes how to resolve the bootable ext4 artifact.
	Image ImageSpec

	// CacheRoot is the filesystem root of the image cache. Required when
	// Image.Digest or Image.Ref is set; ignored when Image.RootfsPath is set.
	CacheRoot string

	// ReachabilityTimeout is the maximum time to wait for the guest agent to
	// become reachable after the VM starts. Defaults to 30 seconds.
	ReachabilityTimeout time.Duration

	// AllowedHosts is the list of hostnames the sandbox may reach through the
	// egress perimeter. Stored frozen on the Envelope and used at boot to mint
	// placeholder credentials. If empty no credentials are seeded.
	AllowedHosts []string

	// Broker is the host-side credential broker used to mint placeholder
	// credentials for AllowedHosts. If nil, credential seeding is skipped even
	// when AllowedHosts is non-empty.
	Broker *cred.Broker

	// Seeder delivers the minted placeholder env file into the guest. If nil,
	// credential seeding is skipped. See NewAgentCopySeeder for the production
	// implementation; tests inject a capture stub.
	Seeder GuestSeeder

	// MemoryMiB is the guest RAM in mebibytes to pass to the driver factory.
	// When zero the driver factory uses its built-in default (512 MiB).
	MemoryMiB uint32

	// VCPUs is the number of virtual CPUs to pass to the driver factory.
	// When zero the driver factory uses its built-in default (1 vCPU).
	VCPUs uint32
}

// CreateAndBoot creates a sandbox record, boots a VM for it, verifies the
// guest agent is reachable, and returns the running sandbox.
//
// On any failure after the record has been created the record is deleted and
// no orphan state is left behind. This includes driver.Start failure and probe
// timeout — either way the caller sees a clear error with no zombie record.
//
// The Driver returned by newDriver is owned exclusively by this call; it is
// not retained by svc. A separate call to svc.Start/Stop uses svc's driver.
// This design allows S2 to pass a CHDriver configured with a per-sandbox
// DiskImagePath without changing the CHDriver constructor signature.
func CreateAndBoot(
	ctx context.Context,
	svc *Service,
	cache *image.Cache,
	newDriver DriverFactory,
	probe ProbeFunc,
	project, name string,
	opts CreateAndBootOptions,
) (domain.Sandbox, error) {
	// ── 1. Resolve ext4 path from the image spec ─────────────────────────────
	ext4Path, resolvedDigest, err := resolveExt4(ctx, opts.Image, cache, opts.CacheRoot)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: %w", project, name, err)
	}

	// ── 2. Guard against duplicate handles (mirrors service.Create) ───────────
	handle := project + "/" + name
	if _, err := svc.store.ResolveByHandle(ctx, handle); err == nil {
		return domain.Sandbox{}, fmt.Errorf("sandbox %q already exists: %w", handle, store.ErrAlreadyExists)
	} else if !errors.Is(err, store.ErrNotFound) {
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot: check handle %q: %w", handle, err)
	}

	// ── 3. Construct per-sandbox driver instance ──────────────────────────────
	bootDrv, err := newDriver(ext4Path)
	if err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: init driver: %w", project, name, err)
	}

	// ── 4. Persist sandbox record ─────────────────────────────────────────────
	id := domain.NewSandboxID()
	sb := domain.Sandbox{
		ID:      id,
		Name:    name,
		Project: project,
		State:   domain.Created,
		Envelope: domain.Envelope{
			ImageDigest:  resolvedDigest,
			AllowedHosts: opts.AllowedHosts, // frozen at creation (P1-S6)
		},
		RemoveOnExit: opts.RemoveOnExit,
	}
	if err := svc.store.Create(ctx, sb); err != nil {
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: create record: %w", project, name, err)
	}

	// ── 5. Boot VM inside store.Update (same locking pattern as service.Start) ─
	//
	// driver.Start is called inside Update so the substrate call and the record
	// write are guarded by the same per-sandbox exclusive flock. This prevents
	// the double-boot race described in service.Start.
	var booted domain.Sandbox
	bootErr := svc.store.Update(ctx, id, func(rec *domain.Sandbox) error {
		// Re-validate inside the lock (authoritative check).
		tr, err := svc.machine.Next(rec.State, lifecycle.TriggerStart)
		if err != nil {
			return fmt.Errorf("re-validate: %w", err)
		}
		instanceID, err := bootDrv.Start(ctx, driver.StartRequest{
			SandboxID:   rec.ID,
			ImageDigest: rec.Envelope.ImageDigest,
		})
		if err != nil {
			return fmt.Errorf("driver: %w", err)
		}
		rec.State = tr.NextState
		rec.InstanceID = instanceID
		rec.StopReason = "" // cleared: sandbox is running
		booted = *rec
		return nil
	})
	if bootErr != nil {
		// Boot failed — delete the record so no orphan exists in Created state.
		_ = svc.store.Delete(ctx, id)
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: boot: %w", project, name, bootErr)
	}

	// ── 6. Reachability probe ─────────────────────────────────────────────────
	if probe != nil {
		timeout := opts.ReachabilityTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		rCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		if err := probe(rCtx, bootDrv, booted.ID); err != nil {
			// Agent unreachable — stop the VM and remove the record.
			_ = bootDrv.Stop(ctx, booted.ID)
			_ = svc.store.Delete(ctx, booted.ID)
			return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: %w: %v",
				project, name, ErrAgentUnreachable, err)
		}
	}

	// ── 7. Seed guest with placeholder credentials (P1-S6) ───────────────────────
	//
	// SeedGuest is a no-op when Broker, Seeder, or AllowedHosts is nil/empty,
	// so existing callers that omit these fields are unaffected.
	//
	// NOTE: service.Start (restart of a stopped sandbox) will also need seeding
	// once it gains a reachability probe — /run is tmpfs and does not survive
	// a guest restart. That wiring is deferred until the restart probe exists.
	if _, err := SeedGuest(ctx, opts.Broker, booted.ID, booted.Envelope.AllowedHosts, opts.Seeder); err != nil {
		_ = bootDrv.Stop(ctx, booted.ID)
		_ = svc.store.Delete(ctx, booted.ID)
		return domain.Sandbox{}, fmt.Errorf("service: create-and-boot %s/%s: seed: %w", project, name, err)
	}

	return booted, nil
}

// resolveExt4 determines the path to a bootable ext4 artifact from spec.
// Returns the path and the digest string to store in Envelope.ImageDigest.
// When spec.RootfsPath is set the digest is empty (no cache entry).
func resolveExt4(
	ctx context.Context,
	spec ImageSpec,
	cache *image.Cache,
	cacheRoot string,
) (ext4Path, imageDigest string, err error) {
	switch {
	case spec.RootfsPath != "":
		// Direct ext4 path — no cache lookup.
		return spec.RootfsPath, "", nil

	case spec.Digest != "":
		d, err := domain.ParseDigest(spec.Digest)
		if err != nil {
			return "", "", fmt.Errorf("resolve image: invalid digest %q: %w", spec.Digest, err)
		}
		if _, err := cache.Get(ctx, d); err != nil {
			return "", "", fmt.Errorf("resolve image: digest %q: %w", spec.Digest, err)
		}
		return filepath.Join(cacheRoot, d.Algo(), d.Hex(), "artifact"), string(d), nil

	case spec.Ref != "":
		// Accept digest strings in the Ref field as a convenience.
		if d, err := domain.ParseDigest(spec.Ref); err == nil {
			if _, err := cache.Get(ctx, d); err != nil {
				return "", "", fmt.Errorf("resolve image: digest %q: %w", spec.Ref, err)
			}
			return filepath.Join(cacheRoot, d.Algo(), d.Hex(), "artifact"), string(d), nil
		}
		// Scan the cache list for a matching human-readable ref.
		imgs, err := cache.List(ctx)
		if err != nil {
			return "", "", fmt.Errorf("resolve image: list cache: %w", err)
		}
		for _, img := range imgs {
			if img.Ref == spec.Ref {
				d := img.Digest
				return filepath.Join(cacheRoot, d.Algo(), d.Hex(), "artifact"), string(d), nil
			}
		}
		return "", "", fmt.Errorf("resolve image: no cached image with ref %q", spec.Ref)

	default:
		return "", "", fmt.Errorf("resolve image: one of Digest, Ref, or RootfsPath must be set")
	}
}
