package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// TokenStore validates bearer tokens.
type TokenStore interface {
	Valid(token string) bool
}

// StaticTokenStore validates against a single static token.
type StaticTokenStore struct {
	token string
}

// NewStaticTokenStore creates a StaticTokenStore.
func NewStaticTokenStore(token string) *StaticTokenStore {
	return &StaticTokenStore{token: token}
}

func (s *StaticTokenStore) Valid(token string) bool {
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) == 1
}

// NetworkListenerConfig holds the fields needed to start the network listener.
type NetworkListenerConfig struct {
	BindAddress string
	Port        int
	TLSMode     string
	Token       string
	TLSCertFile string
	TLSKeyFile  string
}

// NetworkListener serves JSON-RPC 2.0 over WebSocket on a TCP address.
type NetworkListener struct {
	cfg        NetworkListenerConfig
	tokens     TokenStore
	dispatcher Dispatcher
	server     *http.Server
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// NewNetworkListener creates a NetworkListener. Returns an error if token is empty.
func NewNetworkListener(cfg NetworkListenerConfig, dispatcher Dispatcher) (*NetworkListener, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("network listener: token must not be empty")
	}
	nl := &NetworkListener{
		cfg:        cfg,
		tokens:     NewStaticTokenStore(cfg.Token),
		dispatcher: dispatcher,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", nl.handleHealthz)
	mux.HandleFunc("/version", nl.handleVersion)
	mux.HandleFunc("/", nl.handleWS)
	nl.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.BindAddress, cfg.Port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return nl, nil
}

// Serve starts the HTTP server and blocks until ctx is cancelled or a fatal error occurs.
func (nl *NetworkListener) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", nl.server.Addr)
	if err != nil {
		return fmt.Errorf("network listener: listen tcp %s: %w", nl.server.Addr, err)
	}

	switch nl.cfg.TLSMode {
	case "auto":
		tlsCfg, err := buildSelfSignedTLS()
		if err != nil {
			return fmt.Errorf("network listener: tls auto: %w", err)
		}
		log.Printf("transport: TLS auto mode: using in-memory self-signed certificate")
		nl.server.TLSConfig = tlsCfg
		ln = tls.NewListener(ln, tlsCfg)
	case "required":
		tlsCfg, err := buildRequiredTLS(nl.cfg.TLSCertFile, nl.cfg.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("network listener: tls required: %w", err)
		}
		nl.server.TLSConfig = tlsCfg
		ln = tls.NewListener(ln, tlsCfg)
	}

	log.Printf("transport: network listener on %s", nl.server.Addr)

	errCh := make(chan error, 1)
	go func() {
		if serveErr := nl.server.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		} else {
			errCh <- nil
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = nl.server.Shutdown(shutCtx)
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// Close shuts down the network listener immediately.
func (nl *NetworkListener) Close() error {
	return nl.server.Close()
}

func buildSelfSignedTLS() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "nexusd-auto"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert := tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
}

func buildRequiredTLS(certFile, keyFile string) (*tls.Config, error) {
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
	}
	log.Printf("transport: TLS required mode: no cert/key files provided, using self-signed cert")
	return buildSelfSignedTLS()
}

func (nl *NetworkListener) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (nl *NetworkListener) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"version":"dev"}`))
}

func (nl *NetworkListener) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if !nl.checkBearer(authHeader) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("transport: ws upgrade: %v", err)
		return
	}
	defer conn.Close()

	nl.serveWSConn(r.Context(), conn)
}

func (nl *NetworkListener) checkBearer(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return nl.tokens.Valid(header[len(prefix):])
}

func (nl *NetworkListener) serveWSConn(ctx context.Context, conn *websocket.Conn) {
	for {
		msgType, raw, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("transport: ws read: %v", err)
			}
			return
		}
		if msgType != websocket.TextMessage {
			continue
		}

		resp := nl.handle(ctx, raw)
		out, err := json.Marshal(resp)
		if err != nil {
			log.Printf("transport: ws marshal response: %v", err)
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, out); err != nil {
			log.Printf("transport: ws write: %v", err)
			return
		}
	}
}

func (nl *NetworkListener) handle(ctx context.Context, raw []byte) rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return rpcResponse{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
		}
	}

	result, err := nl.dispatcher.Dispatch(ctx, req.Method, req.Params)
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
