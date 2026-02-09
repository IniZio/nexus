package transport

import (
	"context"
	"io"
	"time"
)

// Transport defines the interface for communication protocols
type Transport interface {
	// Connect establishes a connection to the target
	Connect(ctx context.Context, target string) error

	// Disconnect closes the connection
	Disconnect(ctx context.Context) error

	// IsConnected returns the connection status
	IsConnected() bool

	// Execute runs a command on the remote target
	Execute(ctx context.Context, cmd *Command) (*Result, error)

	// Upload transfers a file to the remote target
	Upload(ctx context.Context, localPath, remotePath string) error

	// Download retrieves a file from the remote target
	Download(ctx context.Context, remotePath, localPath string) error

	// GetInfo returns transport information
	GetInfo() *Info
}

// Command represents a command to execute
type Command struct {
	Cmd           []string          `json:"cmd"`
	Env           map[string]string `json:"env,omitempty"`
	WorkingDir    string            `json:"working_dir,omitempty"`
	Timeout       time.Duration     `json:"timeout,omitempty"`
	User          string            `json:"user,omitempty"`
	Stdin         io.Reader         `json:"-"`
	Stdout        io.Writer         `json:"-"`
	Stderr        io.Writer         `json:"-"`
	CaptureOutput bool              `json:"capture_output,omitempty"`
}

// Result represents the result of a command execution
type Result struct {
	ExitCode int           `json:"exit_code"`
	Output   string        `json:"output,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
	Start    time.Time     `json:"start"`
	End      time.Time     `json:"end"`
}

// Info contains transport metadata
type Info struct {
	Protocol   string            `json:"protocol"`
	Target     string            `json:"target"`
	Connected  bool              `json:"connected"`
	CreatedAt  time.Time         `json:"created_at"`
	LastUsed   time.Time         `json:"last_used"`
	Properties map[string]string `json:"properties,omitempty"`
}

// Config holds transport configuration
type Config struct {
	Protocol    string           `yaml:"protocol"`
	Target      string           `yaml:"target"`
	Auth        AuthConfig       `yaml:"auth,omitempty"`
	Timeout     time.Duration    `yaml:"timeout,omitempty"`
	RetryPolicy RetryPolicy      `yaml:"retry,omitempty"`
	Connection  ConnectionConfig `yaml:"connection,omitempty"`
	Security    SecurityConfig   `yaml:"security,omitempty"`
}

// AuthConfig defines authentication settings
type AuthConfig struct {
	Type     string            `yaml:"type"` // ssh_key, password, token, certificate
	Username string            `yaml:"username,omitempty"`
	Password string            `yaml:"password,omitempty"`
	KeyPath  string            `yaml:"key_path,omitempty"`
	KeyData  []byte            `yaml:"key_data,omitempty"`
	Token    string            `yaml:"token,omitempty"`
	Headers  map[string]string `yaml:"headers,omitempty"`
	CertPath string            `yaml:"cert_path,omitempty"`
	CertData []byte            `yaml:"cert_data,omitempty"`
}

// RetryPolicy defines retry behavior
type RetryPolicy struct {
	MaxRetries      int           `yaml:"max_retries"`
	InitialDelay    time.Duration `yaml:"initial_delay"`
	MaxDelay        time.Duration `yaml:"max_delay"`
	BackoffFactor   float64       `yaml:"backoff_factor"`
	RetriableErrors []string      `yaml:"retriable_errors,omitempty"`
}

// ConnectionConfig defines connection behavior
type ConnectionConfig struct {
	MaxConns     int           `yaml:"max_conns,omitempty"`
	MaxIdle      int           `yaml:"max_idle,omitempty"`
	MaxLifetime  time.Duration `yaml:"max_lifetime,omitempty"`
	IdleTimeout  time.Duration `yaml:"idle_timeout,omitempty"`
	KeepAlive    bool          `yaml:"keep_alive,omitempty"`
	KeepAliveInt time.Duration `yaml:"keep_alive_interval,omitempty"`
}

// SecurityConfig defines security settings
type SecurityConfig struct {
	StrictHostKeyChecking bool     `yaml:"strict_host_key_checking,omitempty"`
	HostKeyAlgorithms     []string `yaml:"host_key_algorithms,omitempty"`
	Ciphers               []string `yaml:"ciphers,omitempty"`
	KEXAlgorithms         []string `yaml:"kex_algorithms,omitempty"`
	VerifyCertificate     bool     `yaml:"verify_certificate,omitempty"`
	CACertPath            string   `yaml:"ca_cert_path,omitempty"`
	CACertData            []byte   `yaml:"ca_cert_data,omitempty"`
	SkipTLSVerify         bool     `yaml:"skip_tls_verify,omitempty"`
}

// Error types
type TransportError struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	Code      int    `json:"code,omitempty"`
	Retryable bool   `json:"retryable"`
}

func (e *TransportError) Error() string {
	return e.Message
}

// Common error types
var (
	ErrNotConnected     = &TransportError{Type: "connection", Message: "not connected", Retryable: false}
	ErrInvalidTarget    = &TransportError{Type: "target", Message: "invalid target", Retryable: false}
	ErrAuthFailed       = &TransportError{Type: "auth", Message: "authentication failed", Retryable: false}
	ErrTimeout          = &TransportError{Type: "timeout", Message: "operation timed out", Retryable: true}
	ErrConnectionFailed = &TransportError{Type: "connection", Message: "connection failed", Retryable: true}
	ErrCommandFailed    = &TransportError{Type: "command", Message: "command execution failed", Retryable: false}
	ErrFileNotFound     = &TransportError{Type: "file", Message: "file not found", Retryable: false}
	ErrPermissionDenied = &TransportError{Type: "permission", Message: "permission denied", Retryable: false}
)
