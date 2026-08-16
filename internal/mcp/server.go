// Package mcp provides an MCP (Model Context Protocol) server over stdio for
// nexus3 sandbox lifecycle management.
//
// # SDK
//
// Uses github.com/modelcontextprotocol/go-sdk v1.7.0. Key API facts verified
// empirically (go doc + source read):
//   - mcp.NewServer(&mcp.Implementation{Name, Version}, nil) *mcp.Server
//   - mcp.AddTool[In, Out any](s, *mcp.Tool, handler) — handler signature:
//     func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)
//   - &mcp.StdioTransport{} — binds os.Stdin / os.Stdout; no configurable fields
//   - server.Run(ctx, transport) error — blocks until stdin EOF, then returns nil
//   - Regular (non-jsonrpc2) errors from handlers are converted to
//     CallToolResult{IsError: true} — they do NOT terminate the session
//
// # Stdout discipline
//
// This package never writes to os.Stdout. The StdioTransport owns stdout
// exclusively while the server is running. All logging must go to os.Stderr.
//
// # Exposed tools
//
//   - sandbox_create  – mint a new sandbox record (project, name, remove_on_exit)
//   - sandbox_list    – list all sandboxes (no args)
//   - sandbox_start   – start a created or stopped sandbox (ref)
//   - sandbox_stop    – stop a running sandbox (ref)
//   - sandbox_pause   – pause a running sandbox (ref)
//   - sandbox_resume  – resume a paused sandbox (ref)
//   - sandbox_remove  – remove a sandbox (ref)
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
)

// SandboxService is the subset of *service.Service consumed by the MCP tools.
// The production adapter in internal/cli/cmd_mcp.go satisfies this interface;
// no code in service/ was modified.
type SandboxService interface {
	Create(ctx context.Context, project, name string, opts service.CreateOptions) (domain.Sandbox, error)
	// CreateAndBoot creates a sandbox record and boots a VM for it in a single
	// step, returning the sandbox in running state. The adapter resolves the
	// image cache root and driver factory from its server-side configuration.
	CreateAndBoot(ctx context.Context, project, name string, opts service.CreateAndBootOptions) (domain.Sandbox, error)
	List(ctx context.Context) ([]domain.Sandbox, error)
	Start(ctx context.Context, ref string) (domain.Sandbox, error)
	Stop(ctx context.Context, ref string) (domain.Sandbox, error)
	Pause(ctx context.Context, ref string) (domain.Sandbox, error)
	Resume(ctx context.Context, ref string) (domain.Sandbox, error)
	Remove(ctx context.Context, ref string) error
}

// sandboxJSON is the wire representation of a sandbox returned by MCP tools.
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

// ── tool input types ──────────────────────────────────────────────────────────

// createArgs holds arguments for the sandbox_create tool.
//
// When none of rootfs_path, digest, or ref is set the tool calls Create
// (store-only, state=created). When any image field is set the tool calls
// CreateAndBoot (create + boot + agent reachability, state=running).
type createArgs struct {
	Project      string `json:"project"        jsonschema:"the project name (required)"`
	Name         string `json:"name"           jsonschema:"the sandbox name (required)"`
	RemoveOnExit bool   `json:"remove_on_exit" jsonschema:"remove sandbox when its primary command exits"`
	// Optional boot fields — provide exactly one; if any is set, CreateAndBoot is used.
	RootfsPath string `json:"rootfs_path,omitempty" jsonschema:"direct path to a raw ext4 rootfs file on the server (optional; triggers boot)"`
	Digest     string `json:"digest,omitempty"      jsonschema:"sha256:<hex> image digest in the server image cache (optional; triggers boot)"`
	Ref        string `json:"ref,omitempty"         jsonschema:"image tag or digest string in the server image cache (optional; triggers boot)"`
	// Optional resource overrides — only used when a boot field is also set.
	MemoryMiB uint32 `json:"memory_mib,omitempty" jsonschema:"guest RAM in MiB (optional; 0 = driver default 512 MiB)"`
	VCPUs     uint32 `json:"vcpus,omitempty"      jsonschema:"number of virtual CPUs (optional; 0 = driver default 1)"`
	// Optional motive association — associates the sandbox with a motive work thread.
	Motive string `json:"motive,omitempty" jsonschema:"motive ID to associate this sandbox with (optional; '' = unassociated)"`
	// NestedVirt opts in to KVM-accelerated nested virtualisation (exposes /dev/kvm in guest).
	// Default false (hardened posture). Only meaningful when a boot field is set.
	NestedVirt bool `json:"nested_virt,omitempty" jsonschema:"expose /dev/kvm inside guest (optional; default false)"`
}

// refArgs holds a single sandbox reference used by start/stop/pause/resume/remove.
type refArgs struct {
	Ref string `json:"ref" jsonschema:"sandbox reference: exact ID, ID prefix, or project/name handle"`
}

// noArgs is used for tools that accept no arguments (sandbox_list).
type noArgs struct{}

// NewServer creates and returns an MCP server with the sandbox lifecycle tools
// registered. The caller runs the server with:
//
//	server.Run(ctx, &gosdk.StdioTransport{})
//
// which blocks until stdin EOF then returns nil (clean shutdown).
func NewServer(svc SandboxService) *gosdk.Server {
	srv := gosdk.NewServer(&gosdk.Implementation{
		Name:    "nexus3",
		Version: "v0.1.0",
	}, nil)
	registerTools(srv, svc)
	return srv
}

// KnownTools returns the names of all MCP tools registered by this server.
// The surface parity check (internal/cli/surface_parity_test.go) calls this
// to assert CLI-MCP surface alignment.
func KnownTools() []string {
	return []string{
		"sandbox_create",
		"sandbox_list",
		"sandbox_start",
		"sandbox_stop",
		"sandbox_pause",
		"sandbox_resume",
		"sandbox_remove",
	}
}

// listMaxResponseBytes caps sandbox_list responses. Lists exceeding this size
// are trimmed and returned with a Truncated header so callers can detect
// omission. 64 KiB is generous for typical sandbox counts (thousands of
// entries) while staying well within MCP message-size limits.
const listMaxResponseBytes = 64 * 1024

// listResultWithCap serialises list, returning successResult when the full
// array fits within maxBytes. When it does not, as many complete items as
// fit are returned via successWithTruncation. At least one item is always
// returned — a zero-item truncated response would be more confusing than a
// slightly-over-budget one.
func listResultWithCap(list []sandboxJSON, maxBytes int64) *gosdk.CallToolResult {
	b, _ := json.Marshal(list)
	totalBytes := int64(len(b))
	if totalBytes <= maxBytes {
		return successResult(list)
	}
	// Find the longest prefix that fits within maxBytes.
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

// registerTools wires each sandbox lifecycle method to an MCP tool on srv.
func registerTools(srv *gosdk.Server, svc SandboxService) {
	// sandbox_create — create a sandbox record, optionally booting it.
	//
	// No image fields → store-only Create (state=created, back-compatible default).
	// Any image field set → CreateAndBoot (create + boot VM + agent probe, state=running).
	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "sandbox_create",
		Description: "Create a sandbox. Without image fields: mint a record in state 'created'. " +
			"With rootfs_path, digest, or ref: create and boot in one step, returning state 'running'. " +
			"Returns the sandbox as JSON.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args createArgs) (*gosdk.CallToolResult, any, error) {
		if args.Project == "" || args.Name == "" {
			return nil, nil, fmt.Errorf("project and name are required")
		}

		// Boot path: any image field triggers CreateAndBoot.
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

		// Record-only path: back-compatible default when no image is specified.
		sb, err := svc.Create(ctx, args.Project, args.Name, service.CreateOptions{
			RemoveOnExit: args.RemoveOnExit,
		})
		if err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(toSandboxJSON(sb)), nil, nil
	})

	// sandbox_list — list all sandbox records.
	// Responses exceeding listMaxResponseBytes are trimmed; the truncated field
	// carries bytes_omitted and total_bytes so callers can distinguish a short
	// list from a capped one (I1-AC2 truncation wire).
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

	// sandbox_start — transition a sandbox to the running state.
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

	// sandbox_stop — terminate a running sandbox.
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

	// sandbox_pause — suspend a running sandbox.
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

	// sandbox_resume — resume a paused sandbox.
	gosdk.AddTool(srv, &gosdk.Tool{
		Name:        "sandbox_resume",
		Description: "Resume a paused sandbox. Returns the updated sandbox as JSON.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args refArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return nil, nil, fmt.Errorf("ref is required")
		}
		sb, err := svc.Resume(ctx, args.Ref)
		if err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(toSandboxJSON(sb)), nil, nil
	})

	// sandbox_remove — delete a sandbox record.
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
}
