package vsock

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"sync"
)

type Server struct {
	port     uint32
	listener net.Listener
	mu       sync.Mutex
	running  bool
	wg       sync.WaitGroup

	HandleRequest func(ctx context.Context, req Request) Response
}

func NewServer(port uint32) *Server {
	return &Server{port: port}
}

func (s *Server) Port() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		if ta, ok := s.listener.Addr().(*net.TCPAddr); ok {
			return uint32(ta.Port)
		}
	}
	return s.port
}

func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Server) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	addr := "127.0.0.1:0"
	if s.port != 0 {
		addr = net.JoinHostPort("127.0.0.1", strconv.FormatUint(uint64(s.port), 10))
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.listener = ln
	s.running = true
	s.mu.Unlock()

	go s.acceptLoop()
	return nil
}

func (s *Server) acceptLoop() {
	for {
		s.mu.Lock()
		ln := s.listener
		s.mu.Unlock()
		if ln == nil {
			return
		}
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			defer c.Close()
			s.handleConnection(c)
		}(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	var req Request
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(Response{Error: "invalid request"})
		return
	}

	ctx := context.Background()
	var resp Response
	if s.HandleRequest != nil {
		resp = s.HandleRequest(ctx, req)
	} else {
		resp = Response{Error: "no handler configured"}
	}
	_ = enc.Encode(resp)
}

func (s *Server) Stop() error {
	s.mu.Lock()
	ln := s.listener
	s.listener = nil
	s.running = false
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	s.wg.Wait()
	return nil
}
