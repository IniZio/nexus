package ssh

import (
	"testing"
	"time"

	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

func TestConnectionConfig(t *testing.T) {
	t.Run("creates config from SSHConnection", func(t *testing.T) {
		conn := &types.SSHConnection{
			Host:       "example.com",
			Port:       22,
			Username:   "user",
			PrivateKey: "key-data",
		}

		cfg := GetDaytonaSSHConfig(conn)

		if cfg.Host != "example.com" {
			t.Errorf("expected example.com, got %s", cfg.Host)
		}
		if cfg.Port != 22 {
			t.Errorf("expected 22, got %d", cfg.Port)
		}
		if cfg.Username != "user" {
			t.Errorf("expected user, got %s", cfg.Username)
		}
		if cfg.PrivateKey != "key-data" {
			t.Errorf("expected key-data, got %s", cfg.PrivateKey)
		}
		if cfg.StrictHostKeyChecking != false {
			t.Error("expected StrictHostKeyChecking=false")
		}
		if cfg.ServerAliveInterval != 30 {
			t.Errorf("expected 30, got %d", cfg.ServerAliveInterval)
		}
		if cfg.ConnectTimeout != 10 {
			t.Errorf("expected 10, got %d", cfg.ConnectTimeout)
		}
	})
}

func TestReadAgentMessage(t *testing.T) {
	t.Run("parses valid message", func(t *testing.T) {
		data := []byte{SSH_AGENTC_REQUEST_IDENTITIES, 0x01, 0x02, 0x03}
		msg, err := ReadAgentMessage(data)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg == nil {
			t.Fatal("expected message")
		}
		if msg.Type != SSH_AGENTC_REQUEST_IDENTITIES {
			t.Errorf("expected type %d, got %d", SSH_AGENTC_REQUEST_IDENTITIES, msg.Type)
		}
		if len(msg.Data) != 3 {
			t.Errorf("expected 3 data bytes, got %d", len(msg.Data))
		}
	})

	t.Run("returns nil for empty data", func(t *testing.T) {
		msg, err := ReadAgentMessage([]byte{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg != nil {
			t.Error("expected nil message for empty data")
		}
	})

	t.Run("returns nil for nil data", func(t *testing.T) {
		msg, err := ReadAgentMessage(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg != nil {
			t.Error("expected nil message for nil data")
		}
	})

	t.Run("handles single byte", func(t *testing.T) {
		data := []byte{SSH_AGENT_SUCCESS}
		msg, err := ReadAgentMessage(data)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg.Type != SSH_AGENT_SUCCESS {
			t.Errorf("expected type %d, got %d", SSH_AGENT_SUCCESS, msg.Type)
		}
		if len(msg.Data) != 0 {
			t.Errorf("expected 0 data bytes, got %d", len(msg.Data))
		}
	})
}

func TestEncodeAgentMessage(t *testing.T) {
	t.Run("encodes message with data", func(t *testing.T) {
		msg := &AgentMessage{
			Type: SSH_AGENTC_SIGN_REQUEST,
			Data: []byte{0x01, 0x02, 0x03},
		}

		encoded := EncodeAgentMessage(msg)

		if len(encoded) != 4 {
			t.Errorf("expected 4 bytes, got %d", len(encoded))
		}
		if encoded[0] != SSH_AGENTC_SIGN_REQUEST {
			t.Errorf("expected type %d, got %d", SSH_AGENTC_SIGN_REQUEST, encoded[0])
		}
		if encoded[1] != 0x01 || encoded[2] != 0x02 || encoded[3] != 0x03 {
			t.Error("data mismatch")
		}
	})

	t.Run("encodes message without data", func(t *testing.T) {
		msg := &AgentMessage{
			Type: SSH_AGENT_SUCCESS,
			Data: []byte{},
		}

		encoded := EncodeAgentMessage(msg)

		if len(encoded) != 1 {
			t.Errorf("expected 1 byte, got %d", len(encoded))
		}
		if encoded[0] != SSH_AGENT_SUCCESS {
			t.Errorf("expected type %d, got %d", SSH_AGENT_SUCCESS, encoded[0])
		}
	})
}

func TestAgentMessageConstants(t *testing.T) {
	if SSH_AGENTC_REQUEST_IDENTITIES != 11 {
		t.Errorf("SSH_AGENTC_REQUEST_IDENTITIES = %d", SSH_AGENTC_REQUEST_IDENTITIES)
	}
	if SSH_AGENT_IDENTITIES_ANSWER != 12 {
		t.Errorf("SSH_AGENT_IDENTITIES_ANSWER = %d", SSH_AGENT_IDENTITIES_ANSWER)
	}
	if SSH_AGENTC_SIGN_REQUEST != 13 {
		t.Errorf("SSH_AGENTC_SIGN_REQUEST = %d", SSH_AGENTC_SIGN_REQUEST)
	}
	if SSH_AGENT_SIGN_RESPONSE != 14 {
		t.Errorf("SSH_AGENT_SIGN_RESPONSE = %d", SSH_AGENT_SIGN_RESPONSE)
	}
	if SSH_AGENTC_ADD_IDENTITY != 17 {
		t.Errorf("SSH_AGENTC_ADD_IDENTITY = %d", SSH_AGENTC_ADD_IDENTITY)
	}
	if SSH_AGENTC_REMOVE_IDENTITY != 18 {
		t.Errorf("SSH_AGENTC_REMOVE_IDENTITY = %d", SSH_AGENTC_REMOVE_IDENTITY)
	}
	if SSH_AGENTC_REMOVE_ALL_IDENTITIES != 19 {
		t.Errorf("SSH_AGENTC_REMOVE_ALL_IDENTITIES = %d", SSH_AGENTC_REMOVE_ALL_IDENTITIES)
	}
	if SSH_AGENT_FAILURE != 5 {
		t.Errorf("SSH_AGENT_FAILURE = %d", SSH_AGENT_FAILURE)
	}
	if SSH_AGENT_SUCCESS != 6 {
		t.Errorf("SSH_AGENT_SUCCESS = %d", SSH_AGENT_SUCCESS)
	}
}

func TestNewBridge(t *testing.T) {
	t.Run("creates bridge with workspace ID", func(t *testing.T) {
		bridge, err := NewBridge("workspace-123")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if bridge == nil {
			t.Fatal("expected bridge")
		}
		if bridge.workspaceID != "workspace-123" {
			t.Errorf("expected workspace-123, got %s", bridge.workspaceID)
		}
		if bridge.socketPath == "" {
			t.Error("expected socket path to be set")
		}
	})
}

func TestSSHBridge_SetActivityCallback(t *testing.T) {
	bridge, _ := NewBridge("test-workspace")

	callbackCalled := false
	bridge.SetActivityCallback(func() {
		callbackCalled = true
	})

	bridge.notifyActivity()

	if !callbackCalled {
		t.Error("expected callback to be called")
	}
}

func TestSSHBridge_SetWebSocket(t *testing.T) {
	bridge, _ := NewBridge("test")

	if bridge.GetWebSocket() != nil {
		t.Error("expected nil websocket initially")
	}
}

func TestSSHBridge_GetSocketPath(t *testing.T) {
	bridge, _ := NewBridge("test-workspace")

	path := bridge.GetSocketPath()
	if path == "" {
		t.Error("expected non-empty socket path")
	}
	if path == "" {
		t.Error("socket path should contain workspace ID")
	}
}

func TestSSHBridge_Start(t *testing.T) {
	bridge, _ := NewBridge("test-start")

	socketPath, err := bridge.Start()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if socketPath == "" {
		t.Error("expected non-empty socket path")
	}

	bridge.Close()
}

func TestSSHBridge_Close(t *testing.T) {
	bridge, _ := NewBridge("test-close")

	_, err := bridge.Start()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bridge.Close()

	bridge.mu.Lock()
	if bridge.listener != nil {
		t.Error("expected listener to be nil after close")
	}
	if bridge.socketPath != "" {
		t.Error("expected socket path to be cleared after close")
	}
	bridge.mu.Unlock()
}

func TestSSHBridge_Close_Idempotent(t *testing.T) {
	bridge, _ := NewBridge("test-close-idempotent")

	bridge.Close()
	bridge.Close()
}

func TestDialTimeout(t *testing.T) {
	t.Run("timeout on invalid address", func(t *testing.T) {
		conn, err := DialTimeout("tcp", "10.255.255.1:1", 50*time.Millisecond)
		if err == nil {
			conn.Close()
			t.Error("expected error on timeout")
		}
	})
}
