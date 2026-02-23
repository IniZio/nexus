package daytona

import (
	"os"
	"testing"
)

func TestLoadAPIKey(t *testing.T) {
	os.Unsetenv("DAYTONA_API_KEY")
	_, err := LoadAPIKey()
	if err == nil {
		t.Error("Expected error when DAYTONA_API_KEY is not set")
	}

	os.Setenv("DAYTONA_API_KEY", "dtn_testkey123")
	key, err := LoadAPIKey()
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if key != "dtn_testkey123" {
		t.Errorf("Expected key 'dtn_testkey123', got %q", key)
	}

	os.Unsetenv("DAYTONA_API_KEY")
}

func TestValidateAPIKey(t *testing.T) {
	tests := []struct {
		key     string
		wantErr bool
	}{
		{"", true},
		{"dtn_validkey123", false},
		{"dtn_", false},
		{"invalid", true},
		{"DTN_uppercase", true},
		{"dtn", true},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			err := ValidateAPIKey(tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateAPIKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}
