package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

func init() {
	Register(Command{
		Name:    "disk",
		Summary: "Report host disk usage by category (usage)",
		Run:     runDisk,
	})
}

const diskUsageText = "disk: usage: disk usage [--json]"

func runDisk(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: diskUsageText}
	}
	verb, verbArgs := args[0], args[1:]
	switch verb {
	case "usage":
		root, err := store.DefaultRoot()
		if err != nil {
			out.EmitError(ErrCodeInternalError, "disk usage: resolve state directory: "+err.Error())
			return nil
		}
		return runDiskUsage(ctx, verbArgs, out, root, currentAgentTag())
	default:
		return &UsageError{Msg: fmt.Sprintf("disk: unknown subcommand %q; valid: usage", verb)}
	}
}

// currentAgentTag mirrors the --file build path: PATH first, then the agent
// binary next to the kernel. No binary => "" (templates cannot be classed stale).
func currentAgentTag() string {
	agentBin, err := exec.LookPath("nexus-agent")
	if err != nil {
		kernelPath, kerr := resolveKernelPath()
		if kerr != nil {
			return ""
		}
		agentBin = filepath.Join(filepath.Dir(kernelPath), "nexus-agent")
	}
	b, err := os.ReadFile(agentBin)
	if err != nil {
		return ""
	}
	return image.BuilderAgentTag(b)
}

func runDiskUsage(ctx context.Context, args []string, out *Output, stateDir, agentTag string) error {
	fs := flag.NewFlagSet("disk usage", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "disk usage: " + err.Error()}
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: diskUsageText}
	}

	c, err := image.NewCache(filepath.Join(stateDir, "images"))
	if err != nil {
		out.EmitError(ErrCodeInternalError, "disk usage: open image cache: "+err.Error())
		return nil
	}
	st, err := store.NewFileStore(stateDir)
	if err != nil {
		out.EmitError(ErrCodeInternalError, "disk usage: open sandbox store: "+err.Error())
		return nil
	}
	rep, err := service.DiskUsage(ctx, stateDir, c, st, agentTag)
	if err != nil {
		out.EmitError(ErrCodeInternalError, err.Error())
		return nil
	}

	if !out.IsJSON() {
		fmt.Fprint(out.Stdout(), renderDiskUsage(rep))
	}
	out.EmitSuccess("disk.usage", rep, fmt.Sprintf("%s on disk, %s reclaimable", humanBytes(rep.TotalBytes), humanBytes(rep.Reclaimable)))
	return nil
}

var diskUsageHeaders = []string{"CATEGORY", "COUNT", "ON DISK", "RECLAIMABLE", "NOTE"}

func renderDiskUsage(rep service.DiskUsageReport) string {
	rows := make([][]string, 0, len(rep.Categories))
	for _, c := range rep.Categories {
		rows = append(rows, []string{
			c.Name,
			fmt.Sprintf("%d", c.Count),
			humanBytes(c.Bytes),
			humanBytes(c.Reclaimable),
			c.Note,
		})
	}
	var b strings.Builder
	b.WriteString(renderTable(diskUsageHeaders, rows))
	fmt.Fprintf(&b, "Total: %s   Reclaimable: %s   Free: %s (floor %s)\n",
		humanBytes(rep.TotalBytes), humanBytes(rep.Reclaimable),
		humanBytes(int64(rep.FreeBytes)), humanBytes(int64(rep.FloorBytes)))
	if rep.BelowFloor {
		b.WriteString("Free space is below the builder floor; builds will fail until space is reclaimed.\n")
	}
	if len(rep.Hints) > 0 {
		fmt.Fprintf(&b, "Next: %s\n", strings.Join(rep.Hints, "; "))
	}
	return b.String()
}
