package mcp

import (
	"context"
	"os"
	"strings"
	"syscall"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const staleWarning = "WARNING: this nexus MCP server is stale: the nexus binary on disk was replaced after " +
	"this server started, so tool behaviour may be out of date. Restart the session or reconnect the nexus MCP server."

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

// staleWatcher detects that the executable at path was replaced since startup.
// It never re-execs; it only reports.
type staleWatcher struct {
	path  string
	start binaryID
	known bool
}

func newStaleWatcher(path string) *staleWatcher {
	path = strings.TrimSuffix(path, " (deleted)")
	id, ok := statBinaryID(path)
	return &staleWatcher{path: path, start: id, known: ok}
}

func (w *staleWatcher) stale() bool {
	if w == nil || !w.known {
		return false
	}
	cur, ok := statBinaryID(w.path)
	return ok && cur != w.start
}

// middleware appends staleWarning to every tools/call result once stale.
func (w *staleWatcher) middleware(next gosdk.MethodHandler) gosdk.MethodHandler {
	return func(ctx context.Context, method string, req gosdk.Request) (gosdk.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil || method != "tools/call" || !w.stale() {
			return res, err
		}
		if ctr, ok := res.(*gosdk.CallToolResult); ok && ctr != nil {
			ctr.Content = append(ctr.Content, &gosdk.TextContent{Text: staleWarning})
		}
		return res, err
	}
}
