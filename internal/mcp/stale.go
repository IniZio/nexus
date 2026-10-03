package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type binaryID struct {
	ino   uint64
	mtime int64
}

func statBinaryID(path string) (binaryID, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return binaryID{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return binaryID{}, false
	}
	return binaryID{ino: uint64(st.Ino), mtime: fi.ModTime().UnixNano()}, true
}

// staleWatcher detects that the executable at path was replaced or deleted since startup.
// Once stale, the middleware refuses every tools/call.
type staleWatcher struct {
	path    string
	start   binaryID
	known   bool
	started time.Time
	pid     int
	exeLink func() string
}

func procExeLink() string {
	l, _ := os.Readlink("/proc/self/exe")
	return l
}

func newStaleWatcher(path string) *staleWatcher {
	path = strings.TrimSuffix(path, " (deleted)")
	id, ok := statBinaryID(path)
	return &staleWatcher{path: path, start: id, known: ok, started: time.Now(), pid: os.Getpid(), exeLink: procExeLink}
}

func (w *staleWatcher) stale() bool {
	if w == nil || !w.known {
		return false
	}
	if w.exeLink != nil && strings.HasSuffix(w.exeLink(), " (deleted)") {
		return true
	}
	cur, ok := statBinaryID(w.path)
	return ok && cur != w.start
}

func (w *staleWatcher) staleMessage() string {
	return fmt.Sprintf("the nexus binary was replaced since this MCP server started (pid %d, started %s); "+
		"it is running stale code and refuses tool calls. Reconnect the nexus MCP server (/mcp, then reconnect) "+
		"or restart the Claude session.", w.pid, w.started.Format(time.RFC3339))
}

// middleware rejects every tools/call with a tool error once stale.
func (w *staleWatcher) middleware(next gosdk.MethodHandler) gosdk.MethodHandler {
	return func(ctx context.Context, method string, req gosdk.Request) (gosdk.Result, error) {
		if method == "tools/call" && w.stale() {
			return &gosdk.CallToolResult{
				IsError: true,
				Content: []gosdk.Content{&gosdk.TextContent{Text: w.staleMessage()}},
			}, nil
		}
		return next(ctx, method, req)
	}
}
