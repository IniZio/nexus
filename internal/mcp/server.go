// Package mcp exposes sandbox lifecycle tools over stdio JSON-RPC (MCP protocol).
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/service"
	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// SandboxService is the subset of *service.Service consumed by the MCP tools.
type SandboxService interface {
	Create(ctx context.Context, project, name string, opts service.CreateOptions) (domain.Sandbox, error)
	CreateAndBoot(ctx context.Context, project, name string, opts service.CreateAndBootOptions) (domain.Sandbox, error)
	List(ctx context.Context) ([]domain.Sandbox, error)
	Start(ctx context.Context, ref string) (domain.Sandbox, error)
	Stop(ctx context.Context, ref string) (domain.Sandbox, error)
	Pause(ctx context.Context, ref string) (domain.Sandbox, error)
	Resume(ctx context.Context, ref string) (domain.Sandbox, error)
	// Hibernate snapshots a running sandbox to disk (idempotent on Hibernated).
	Hibernate(ctx context.Context, ref string) (HibernateResult, error)
	// ResumeSandbox resumes a Paused or Hibernated sandbox; mode is "" (copy
	// default), "copy" or "ondemand".
	ResumeSandbox(ctx context.Context, ref, mode string, noColdFallback bool) (ResumeResult, error)
	Remove(ctx context.Context, ref string) error
	Exec(ctx context.Context, ref string, argv []string, env map[string]string, cwd, stdin string) (exitCode int32, stdout, stderr string, err error)
	RunEphemeral(ctx context.Context, project, name string, opts service.CreateAndBootOptions, argv []string, env map[string]string, cwd, stdin string) (exitCode int32, stdout, stderr string, err error)
}

type sandboxJSON struct {
	ID           string `json:"id"`
	Project      string `json:"project"`
	Name         string `json:"name"`
	Handle       string `json:"handle"`
	State        string `json:"state"`
	RemoveOnExit bool   `json:"remove_on_exit,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
}

func toSandboxJSON(sb domain.Sandbox) sandboxJSON {
	return sandboxJSON{
		ID:           sb.ID.String(),
		Project:      sb.Project,
		Name:         sb.Name,
		Handle:       sb.Handle(),
		State:        sb.State.String(),
		RemoveOnExit: sb.RemoveOnExit,
		StopReason:   string(sb.StopReason),
	}
}

func toSandboxList(sbs []domain.Sandbox) []sandboxJSON {
	out := make([]sandboxJSON, len(sbs))
	for i, sb := range sbs {
		out[i] = toSandboxJSON(sb)
	}
	return out
}

// HibernateResult is the sandbox_hibernate result; fields mirror the CLI
// `hibernate --json` contract.
type HibernateResult struct {
	ID                  string `json:"id"`
	State               string `json:"state"`
	Already             bool   `json:"already"`
	PauseMs             int64  `json:"pause_ms"`
	SnapshotMs          int64  `json:"snapshot_ms"`
	TotalMs             int64  `json:"total_ms"`
	SnapshotBytes       int64  `json:"snapshot_bytes"`
	SnapshotBytesOnDisk int64  `json:"snapshot_bytes_on_disk"`
	SnapshotDir         string `json:"snapshot_dir"`
}

// ResumeResult is the sandbox_resume result; fields mirror the CLI
// `resume --json` contract.
type ResumeResult struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	Already        bool   `json:"already"`
	ResumedFrom    string `json:"resumed_from,omitempty"`
	RestoreMode    string `json:"restore_mode,omitempty"`
	RestoreMs      int64  `json:"restore_ms"`
	AgentReadyMs   int64  `json:"agent_ready_ms"`
	TotalMs        int64  `json:"total_ms"`
	FallbackReason string `json:"fallback_reason,omitempty"`
	ClockSkewMs    int64  `json:"clock_skew_ms,omitempty"`
}

type resumeArgs struct {
	Ref            string `json:"ref"                       jsonschema:"sandbox reference: exact ID, ID prefix, or project/name handle"`
	RestoreMode    string `json:"restore_mode,omitempty"    jsonschema:"snapshot restore mode for a hibernated sandbox: copy (default) or ondemand"`
	NoColdFallback bool   `json:"no_cold_fallback,omitempty" jsonschema:"fail instead of cold-starting when snapshot restore fails"`
}

type createArgs struct {
	Project      string `json:"project"        jsonschema:"the project name (required)"`
	Name         string `json:"name"           jsonschema:"the sandbox name (required)"`
	RemoveOnExit bool   `json:"remove_on_exit" jsonschema:"remove sandbox when its primary command exits"`
	RootfsPath   string `json:"rootfs_path,omitempty" jsonschema:"direct path to a raw ext4 rootfs file on the server (optional; triggers boot)"`
	Digest       string `json:"digest,omitempty"      jsonschema:"sha256:<hex> image digest in the server image cache (optional; triggers boot)"`
	Ref          string `json:"ref,omitempty"         jsonschema:"image tag or digest string in the server image cache (optional; triggers boot)"`
	MemoryMiB    uint32 `json:"memory_mib,omitempty" jsonschema:"guest RAM in MiB (optional; 0 = driver default 512 MiB)"`
	VCPUs        uint32 `json:"vcpus,omitempty"      jsonschema:"number of virtual CPUs (optional; 0 = driver default 1)"`
	Motive       string `json:"motive,omitempty" jsonschema:"motive ID to associate this sandbox with (optional; '' = unassociated)"`
	NestedVirt   bool   `json:"nested_virt,omitempty" jsonschema:"expose /dev/kvm inside guest (optional; default false)"`
}

type refArgs struct {
	Ref string `json:"ref" jsonschema:"sandbox reference: exact ID, ID prefix, or project/name handle"`
}

type noArgs struct{}

type execArgs struct {
	Ref   string            `json:"ref"             jsonschema:"sandbox reference: exact ID, ID prefix, or project/name handle (required)"`
	Argv  []string          `json:"argv"            jsonschema:"command and arguments to run in the guest (required)"`
	Env   map[string]string `json:"env,omitempty"   jsonschema:"additional environment variables as key→value map (optional)"`
	Cwd   string            `json:"cwd,omitempty"   jsonschema:"working directory inside the guest (optional; default: agent default)"`
	Stdin string            `json:"stdin,omitempty" jsonschema:"data to pipe to the command's stdin (optional)"`
}

type runArgs struct {
	Project    string            `json:"project"              jsonschema:"project name (required)"`
	Name       string            `json:"name"                 jsonschema:"sandbox name (required)"`
	RootfsPath string            `json:"rootfs_path,omitempty" jsonschema:"direct path to a raw ext4 rootfs file on the server (optional)"`
	Digest     string            `json:"digest,omitempty"      jsonschema:"sha256:<hex> image digest in the server image cache (optional)"`
	Ref        string            `json:"ref,omitempty"         jsonschema:"image tag or digest string in the server image cache (optional)"`
	MemoryMiB  uint32            `json:"memory_mib,omitempty"  jsonschema:"guest RAM in MiB (optional; 0 = driver default 512 MiB)"`
	VCPUs      uint32            `json:"vcpus,omitempty"       jsonschema:"number of virtual CPUs (optional; 0 = driver default 1)"`
	NestedVirt bool              `json:"nested_virt,omitempty" jsonschema:"expose /dev/kvm inside guest (optional; default false)"`
	Argv       []string          `json:"argv"                 jsonschema:"command and arguments to run in the guest (required)"`
	Env        map[string]string `json:"env,omitempty"        jsonschema:"additional environment variables as key→value map (optional)"`
	Cwd        string            `json:"cwd,omitempty"        jsonschema:"working directory inside the guest (optional)"`
	Stdin      string            `json:"stdin,omitempty"      jsonschema:"data to pipe to the command's stdin (optional)"`
}

type execResult struct {
	ExitCode int32  `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// NewServer creates an MCP server with all sandbox lifecycle and delegate tools registered.
func NewServer(svc SandboxService) *gosdk.Server {
	exe, _ := os.Executable()
	return newServer(svc, newStaleWatcher(exe))
}

func newServer(svc SandboxService, sw *staleWatcher) *gosdk.Server {
	srv := gosdk.NewServer(&gosdk.Implementation{
		Name:    "nexus",
		Version: "v0.1.0",
	}, nil)
	registerTools(srv, svc)
	registerDelegateTools(srv, svc)
	srv.AddReceivingMiddleware(sw.middleware)
	return srv
}

// KnownTools returns the names of all MCP tools registered by this server.
func KnownTools() []string {
	return []string{
		"sandbox_create",
		"sandbox_list",
		"sandbox_start",
		"sandbox_stop",
		"sandbox_pause",
		"sandbox_hibernate",
		"sandbox_resume",
		"sandbox_remove",
		"sandbox_exec",
		"sandbox_run",
		"delegate_worktree_create",
		"delegate_agent_dispatch",
		"delegate_agent_poll",
		"delegate_agent_wait",
		"delegate_agent_followup",
		"delegate_teardown",
	}
}

const listMaxResponseBytes = 64 * 1024

func listResultWithCap(list []sandboxJSON, maxBytes int64) *gosdk.CallToolResult {
	b, _ := json.Marshal(list)
	totalBytes := int64(len(b))
	if totalBytes <= maxBytes {
		return successResult(list)
	}
	trimmed := list[:0]
	for i := range list {
		candidate := list[:i+1]
		cb, _ := json.Marshal(candidate)
		if int64(len(cb)) > maxBytes && i > 0 {
			break
		}
		trimmed = candidate
	}
	tb, _ := json.Marshal(trimmed)
	return successWithTruncation(trimmed, Truncated{
		BytesOmitted: totalBytes - int64(len(tb)),
		TotalBytes:   totalBytes,
	})
}

func registerTools(srv *gosdk.Server, svc SandboxService) {
	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "sandbox_create",
		Description: "Create a sandbox. Without image fields: mint a record in state 'created'. " +
			"With rootfs_path, digest, or ref: create and boot in one step, returning state 'running'. " +
			"Returns the sandbox as JSON.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args createArgs) (*gosdk.CallToolResult, any, error) {
		if args.Project == "" || args.Name == "" {
			return nil, nil, fmt.Errorf("project and name are required")
		}
		if args.RootfsPath != "" || args.Digest != "" || args.Ref != "" {
			var bootLabels map[string]string
			if args.Motive != "" {
				bootLabels = map[string]string{"motive": args.Motive}
			}
			sb, err := svc.CreateAndBoot(ctx, args.Project, args.Name, service.CreateAndBootOptions{
				Labels:       bootLabels,
				RemoveOnExit: args.RemoveOnExit,
				Image: service.ImageSpec{
					RootfsPath: args.RootfsPath,
					Digest:     args.Digest,
					Ref:        args.Ref,
				},
				MemoryMiB:  args.MemoryMiB,
				VCPUs:      args.VCPUs,
				NestedVirt: args.NestedVirt,
			})
			if err != nil {
				return errorResult(err), nil, nil
			}
			return successResult(toSandboxJSON(sb)), nil, nil
		}
		sb, err := svc.Create(ctx, args.Project, args.Name, service.CreateOptions{
			RemoveOnExit: args.RemoveOnExit,
		})
		if err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(toSandboxJSON(sb)), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "sandbox_list",
		Description: "List all sandboxes. Returns a JSON array of sandbox objects. " +
			"Large lists are capped at 64 KiB; check truncated.bytes_omitted.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, _ noArgs) (*gosdk.CallToolResult, any, error) {
		sbs, err := svc.List(ctx)
		if err != nil {
			return errorResult(err), nil, nil
		}
		return listResultWithCap(toSandboxList(sbs), listMaxResponseBytes), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name:        "sandbox_start",
		Description: "Start a created or stopped sandbox. Returns the updated sandbox as JSON.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args refArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return nil, nil, fmt.Errorf("ref is required")
		}
		sb, err := svc.Start(ctx, args.Ref)
		if err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(toSandboxJSON(sb)), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name:        "sandbox_stop",
		Description: "Stop a running sandbox. Returns the updated sandbox as JSON.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args refArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return nil, nil, fmt.Errorf("ref is required")
		}
		sb, err := svc.Stop(ctx, args.Ref)
		if err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(toSandboxJSON(sb)), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name:        "sandbox_pause",
		Description: "Pause a running sandbox. Returns the updated sandbox as JSON.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args refArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return nil, nil, fmt.Errorf("ref is required")
		}
		sb, err := svc.Pause(ctx, args.Ref)
		if err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(toSandboxJSON(sb)), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name:        "sandbox_hibernate",
		Description: "Snapshot a running sandbox to disk and free its RAM (cloud-hypervisor only). Returns {id,state,already,pause_ms,snapshot_ms,total_ms,snapshot_bytes,snapshot_bytes_on_disk,snapshot_dir}. Error codes: hibernate_unsupported, hibernate_refused, illegal_transition, snapshot_failed. sandbox_resume restores it.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args refArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return nil, nil, fmt.Errorf("ref is required")
		}
		res, err := svc.Hibernate(ctx, args.Ref)
		if err != nil {
			return hibernateErrorResult(err), nil, nil
		}
		return successResult(res), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name:        "sandbox_resume",
		Description: "Resume a paused or hibernated sandbox. Returns {id,state,already,resumed_from,restore_mode,restore_ms,agent_ready_ms,total_ms,fallback_reason,clock_skew_ms}. Optional restore_mode (copy|ondemand) and no_cold_fallback apply to hibernated sandboxes. Error codes: hibernate_unsupported, hibernate_refused, illegal_transition, snapshot_failed.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args resumeArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return nil, nil, fmt.Errorf("ref is required")
		}
		if m := args.RestoreMode; m != "" && m != "copy" && m != "ondemand" {
			return nil, nil, fmt.Errorf("restore_mode %q: want copy or ondemand", m)
		}
		res, err := svc.ResumeSandbox(ctx, args.Ref, args.RestoreMode, args.NoColdFallback)
		if err != nil {
			return hibernateErrorResult(err), nil, nil
		}
		return successResult(res), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name:        "sandbox_remove",
		Description: "Remove a sandbox. Returns {\"removed\":true} on success.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args refArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return nil, nil, fmt.Errorf("ref is required")
		}
		if err := svc.Remove(ctx, args.Ref); err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(map[string]bool{"removed": true}), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "sandbox_exec",
		Description: "Execute a command in an existing running sandbox. " +
			"Returns {exit_code, stdout, stderr}.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args execArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return nil, nil, fmt.Errorf("ref is required")
		}
		if len(args.Argv) == 0 {
			return nil, nil, fmt.Errorf("argv is required")
		}
		code, stdout, stderr, err := svc.Exec(ctx, args.Ref, args.Argv, args.Env, args.Cwd, args.Stdin)
		if err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(execResult{ExitCode: code, Stdout: stdout, Stderr: stderr}), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "sandbox_run",
		Description: "Create a sandbox, boot it, execute a command, remove it. " +
			"Returns {exit_code, stdout, stderr}. The sandbox is removed even on error.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args runArgs) (*gosdk.CallToolResult, any, error) {
		if args.Project == "" || args.Name == "" {
			return nil, nil, fmt.Errorf("project and name are required")
		}
		if len(args.Argv) == 0 {
			return nil, nil, fmt.Errorf("argv is required")
		}
		opts := service.CreateAndBootOptions{
			Image: service.ImageSpec{
				RootfsPath: args.RootfsPath,
				Digest:     args.Digest,
				Ref:        args.Ref,
			},
			MemoryMiB:  args.MemoryMiB,
			VCPUs:      args.VCPUs,
			NestedVirt: args.NestedVirt,
		}
		code, stdout, stderr, err := svc.RunEphemeral(ctx, args.Project, args.Name, opts, args.Argv, args.Env, args.Cwd, args.Stdin)
		if err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(execResult{ExitCode: code, Stdout: stdout, Stderr: stderr}), nil, nil
	})
}
