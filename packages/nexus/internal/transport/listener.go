// Package transport provides Unix-socket and TCP listeners for JSON-RPC.
package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"

	"github.com/inizio/nexus/packages/nexus/internal/rpc/registry"
)

// rpcRequest is the incoming JSON-RPC 2.0 envelope.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// rpcResponse is the outgoing JSON-RPC 2.0 envelope.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Dispatcher knows how to route a method + params to a handler.
type Dispatcher interface {
	Dispatch(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// Listener accepts newline-delimited JSON-RPC connections on a Unix socket.
type Listener struct {
	socketPath string
	dispatcher Dispatcher
	ln         net.Listener
	wg         sync.WaitGroup
	once       sync.Once
	closeCh    chan struct{}
}

// NewListener creates a Listener that will serve on socketPath.
func NewListener(socketPath string, reg *registry.MapRegistry) *Listener {
	return &Listener{
		socketPath: socketPath,
		dispatcher: reg,
		closeCh:    make(chan struct{}),
	}
}

// Serve starts accepting connections. It blocks until ctx is done or Close is called.
func (l *Listener) Serve(ctx context.Context) error {
	// Remove stale socket file.
	_ = os.Remove(l.socketPath)

	ln, err := net.Listen("unix", l.socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", l.socketPath, err)
	}
	l.ln = ln

	go func() {
		select {
		case <-ctx.Done():
		case <-l.closeCh:
		}
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				l.wg.Wait()
				return ctx.Err()
			case <-l.closeCh:
				l.wg.Wait()
				return nil
			default:
				log.Printf("transport: accept error: %v", err)
				l.wg.Wait()
				return err
			}
		}
		l.wg.Add(1)
		go func(c net.Conn) {
			defer l.wg.Done()
			l.serveConn(ctx, c)
		}(conn)
	}
}

// Close shuts down the listener.
func (l *Listener) Close() error {
	l.once.Do(func() { close(l.closeCh) })
	if l.ln != nil {
		return l.ln.Close()
	}
	return nil
}

// serveConn reads newline-delimited JSON-RPC requests and writes responses.
func (l *Listener) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	enc := json.NewEncoder(conn)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		resp := l.handle(ctx, line)
		if err := enc.Encode(resp); err != nil && err != io.EOF {
			log.Printf("transport: encode response: %v", err)
			return
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		log.Printf("transport: read conn: %v", err)
	}
}

func (l *Listener) handle(ctx context.Context, raw []byte) rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return rpcResponse{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
		}
	}

	result, err := l.dispatcher.Dispatch(ctx, req.Method, req.Params)
	if err != nil {
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32603, Message: err.Error()},
		}
	}
	return rpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}
