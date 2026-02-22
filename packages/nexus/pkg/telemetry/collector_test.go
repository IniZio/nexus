package telemetry

import (
	"os"
	"testing"
	"time"
)

func TestNewCollector(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	config := Config{
		Enabled:             true,
		Anonymize:           true,
		RetentionDays:       30,
		MaxEventsPerSession: 1000,
	}

	collector, err := NewCollector(tmpFile.Name(), config)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	if collector == nil {
		t.Error("Collector should not be nil")
	}
}

func TestCollector_RecordCommand(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	config := Config{Enabled: true, Anonymize: true}
	collector, err := NewCollector(tmpFile.Name(), config)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	sessionID, err := collector.RecordSessionStart()
	if err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}
	if sessionID == "" {
		t.Error("Session ID should not be empty")
	}

	err = collector.RecordCommand("build", []string{"--prod"}, 5*time.Second, true, nil)
	if err != nil {
		t.Errorf("RecordCommand failed: %v", err)
	}

	err = collector.RecordCommand("start", []string{"-d"}, 10*time.Second, false, &testError{msg: "port 3000 already in use"})
	if err != nil {
		t.Errorf("RecordCommand with error failed: %v", err)
	}
}

func TestCollector_RecordWorkspace(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	config := Config{Enabled: true, Anonymize: true}
	collector, err := NewCollector(tmpFile.Name(), config)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	collector.RecordSessionStart()

	err = collector.RecordWorkspace("create", "my-workspace", "nodejs", 3000)
	if err != nil {
		t.Errorf("RecordWorkspace create failed: %v", err)
	}

	err = collector.RecordWorkspace("up", "my-workspace", "nodejs", 3000)
	if err != nil {
		t.Errorf("RecordWorkspace up failed: %v", err)
	}

	err = collector.RecordWorkspace("destroy", "my-workspace", "nodejs", 0)
	if err != nil {
		t.Errorf("RecordWorkspace destroy failed: %v", err)
	}
}

func TestCollector_RecordTask(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	config := Config{Enabled: true, Anonymize: true}
	collector, err := NewCollector(tmpFile.Name(), config)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	collector.RecordSessionStart()

	err = collector.RecordTask("create", "task-123", 0)
	if err != nil {
		t.Errorf("RecordTask create failed: %v", err)
	}

	err = collector.RecordTask("complete", "task-123", 30*time.Second)
	if err != nil {
		t.Errorf("RecordTask complete failed: %v", err)
	}
}

func TestCollector_SessionLifecycle(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	config := Config{Enabled: true}
	collector, err := NewCollector(tmpFile.Name(), config)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	sessionID, err := collector.RecordSessionStart()
	if err != nil {
		t.Fatalf("RecordSessionStart failed: %v", err)
	}

	currentSession := collector.GetCurrentSession()
	if currentSession != sessionID {
		t.Errorf("Expected current session %s, got %s", sessionID, currentSession)
	}

	err = collector.RecordCommand("test", nil, time.Second, true, nil)
	if err != nil {
		t.Errorf("RecordCommand failed: %v", err)
	}

	err = collector.RecordSessionEnd("Great tool!")
	if err != nil {
		t.Errorf("RecordSessionEnd failed: %v", err)
	}

	currentSession = collector.GetCurrentSession()
	if currentSession != "" {
		t.Error("Current session should be empty after ending")
	}
}

func TestCollector_Disabled(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	config := Config{Enabled: false}
	collector, err := NewCollector(tmpFile.Name(), config)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	err = collector.RecordCommand("build", nil, time.Second, true, nil)
	if err != nil {
		t.Errorf("RecordCommand should not fail when disabled: %v", err)
	}

	sessionID, err := collector.RecordSessionStart()
	if err != nil {
		t.Fatalf("RecordSessionStart should not fail when disabled: %v", err)
	}
	// When disabled, session ID may still be generated but events won't be saved
	_ = sessionID
}

func TestCollector_GetStats(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "telemetry_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	config := Config{Enabled: true}
	collector, err := NewCollector(tmpFile.Name(), config)
	if err != nil {
		t.Fatalf("Failed to create collector: %v", err)
	}
	defer collector.Close()

	collector.RecordSessionStart()
	collector.RecordCommand("build", nil, 5*time.Second, true, nil)
	collector.RecordCommand("start", nil, 10*time.Second, false, &testError{msg: "docker daemon not running"})
	collector.RecordCommand("test", nil, 3*time.Second, true, nil)
	collector.RecordSessionEnd("")

	// Use 365 days to cover all test data
	stats, err := collector.GetStats(365)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if stats.TotalCommands != 3 {
		t.Errorf("Expected 3 commands, got %d", stats.TotalCommands)
	}

	if stats.SuccessRate < 60 || stats.SuccessRate > 70 {
		t.Errorf("Expected success rate between 60-70%%, got %.1f%%", stats.SuccessRate)
	}
}

func TestHashString(t *testing.T) {
	tests := []struct {
		input       string
		expectedLen int
	}{
		{"hello", 16},
		{"world", 16},
		{"same-input", 16},
	}

	for _, tt := range tests {
		result := hashString(tt.input)
		if len(result) != tt.expectedLen {
			t.Errorf("hashString(%s) = %s (len %d), expected %d chars", tt.input, result, len(result), tt.expectedLen)
		}
	}
}

func TestAnonymizeArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected []string
	}{
		{
			name:     "paths are anonymized",
			args:     []string{"/home/user/project", "build"},
			expected: []string{"[path]", "build"},
		},
		{
			name:     "tokens are anonymized",
			args:     []string{"ghp_token123", "deploy"},
			expected: []string{"[token]", "deploy"},
		},
		{
			name:     "secrets are anonymized",
			args:     []string{"api_key=secret123", "run"},
			expected: []string{"[secret]", "run"},
		},
		{
			name:     "env vars are anonymized",
			args:     []string{"$HOME", "echo"},
			expected: []string{"[env]", "echo"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := anonymizeArgs(tt.args)
			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d args, got %d", len(tt.expected), len(result))
				return
			}
			for i, arg := range result {
				if arg != tt.expected[i] {
					t.Errorf("Expected arg[%d] = %s, got %s", i, tt.expected[i], arg)
				}
			}
		})
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"port conflict", &testError{msg: "port 3000 is already in use"}, "port_conflict"},
		{"docker error", &testError{msg: "docker: command not found"}, "docker_error"},
		{"ssh error", &testError{msg: "ssh: connection refused"}, "ssh_error"},
		{"git error", &testError{msg: "fatal: not a git repository"}, "git_error"},
		{"permission error", &testError{msg: "permission denied: access denied"}, "permission_error"},
		{"timeout error", &testError{msg: "request timeout"}, "timeout_error"},
		{"network error", &testError{msg: "network is unreachable"}, "network_error"},
		{"file not found", &testError{msg: "file not found: config.yaml"}, "file_not_found"},
		{"template error", &testError{msg: "template parsing failed"}, "template_error"},
		{"unknown error", &testError{msg: "something unexpected happened"}, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyError(tt.err)
			if result != tt.expected {
				t.Errorf("classifyError(%v) = %s, expected %s", tt.err, result, tt.expected)
			}
		})
	}
}

func TestClassifyError_Nil(t *testing.T) {
	result := classifyError(nil)
	if result != "" {
		t.Errorf("classifyError(nil) = %s, expected empty string", result)
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
