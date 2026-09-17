package portfwd

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
)

type ConnRunner func(argv []string, conn net.Conn) (kill io.Closer, done <-chan struct{}, err error)

// OSConnRunner starts argv[0] with the connection as stdin/stdout and kills it on Close.
func OSConnRunner(argv []string, conn net.Conn) (io.Closer, <-chan struct{}, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = conn
	cmd.Stdout = conn
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := cmd.Wait(); err != nil {
			s := strings.TrimRight(stderrBuf.String(), "\n")
			if i := strings.LastIndexByte(s, '\n'); i >= 0 {
				s = s[i+1:]
			}
			if s != "" {
				slog.Warn("portfwd: ssh -W exited non-zero", "err", err, "stderr", s)
			}
		}
		conn.Close()
	}()
	return &processKiller{cmd.Process}, done, nil
}

type processKiller struct{ p *os.Process }

func (k *processKiller) Close() error { return k.p.Kill() }

type LocalForward struct {
	ControlPath string
	SSHHost     string
	Port        uint16
	RunConn     ConnRunner
	ListenFunc  func(string, string) (net.Listener, error)

	mu       sync.Mutex
	closed   bool
	listener net.Listener
	conns    []*liveConn
}

type liveConn struct {
	conn net.Conn
	kill io.Closer
}

// Open binds 127.0.0.1:<Port> and starts the accept loop.
func (lf *LocalForward) Open() error {
	listenFn := lf.ListenFunc
	if listenFn == nil {
		listenFn = net.Listen
	}
	ln, err := listenFn("tcp", fmt.Sprintf("127.0.0.1:%d", lf.Port))
	if err != nil {
		return err
	}
	lf.mu.Lock()
	lf.listener = ln
	lf.mu.Unlock()
	go lf.acceptLoop()
	return nil
}

func (lf *LocalForward) acceptLoop() {
	lf.mu.Lock()
	ln := lf.listener
	lf.mu.Unlock()
	if ln == nil {
		return
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go lf.handleConn(conn)
	}
}

func (lf *LocalForward) handleConn(conn net.Conn) {
	argv := []string{
		"ssh", "-S", lf.ControlPath,
		"-o", "BatchMode=yes",
		"-W", fmt.Sprintf("127.0.0.1:%d", lf.Port),
		lf.SSHHost,
	}
	runConn := lf.RunConn
	if runConn == nil {
		runConn = OSConnRunner
	}
	kill, done, err := runConn(argv, conn)
	if err != nil {
		conn.Close()
		return
	}
	lc := &liveConn{conn: conn, kill: kill}
	lf.mu.Lock()
	if lf.closed {
		lf.mu.Unlock()
		kill.Close()
		conn.Close()
		return
	}
	lf.conns = append(lf.conns, lc)
	lf.mu.Unlock()
	if done != nil {
		go func() {
			<-done
			lf.mu.Lock()
			for i, c := range lf.conns {
				if c == lc {
					lf.conns = append(lf.conns[:i], lf.conns[i+1:]...)
					break
				}
			}
			lf.mu.Unlock()
		}()
	}
}

// Close stops accepting, kills all proxy children, and closes all connections.
func (lf *LocalForward) Close() error {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	lf.closed = true
	if lf.listener != nil {
		lf.listener.Close()
		lf.listener = nil
	}
	for _, lc := range lf.conns {
		lc.kill.Close()
		lc.conn.Close()
	}
	lf.conns = nil
	return nil
}

// ConnCount returns the number of currently tracked active connections.
func (lf *LocalForward) ConnCount() int {
	lf.mu.Lock()
	defer lf.mu.Unlock()
	return len(lf.conns)
}
