package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"

	"github.com/inizio/nexus/packages/nexus/pkg/secrets/vending"
	"github.com/inizio/nexus/packages/nexus/pkg/secrets/vsock"
)

type Server struct {
	port     uint32
	service  *vending.Service
	listener net.Listener
	mu       sync.Mutex
	running  bool
}

func New(service *vending.Service, port uint32) *Server {
	return &Server{
		port:    port,
		service: service,
	}
}

func (s *Server) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.listener = listener
	s.running = true
	s.mu.Unlock()
	go s.acceptLoop()
	return nil
}

func (s *Server) Stop() error {
	s.mu.Lock()
	ln := s.listener
	s.listener = nil
	s.running = false
	s.mu.Unlock()
	if ln != nil {
		return ln.Close()
	}
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
		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	var req vsock.Request
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(vsock.Response{Error: "invalid request"})
		return
	}

	ctx := context.Background()
	token, err := s.service.GetToken(ctx, req.Provider)

	var resp vsock.Response
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Token = token.Value
		resp.ExpiresAt = token.ExpiresAt
	}
	_ = enc.Encode(resp)
}
