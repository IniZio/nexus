package workspace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestManager_validateCreate(t *testing.T) {
	mockProv := newMockProvider()
	manager := NewManager(mockProv)

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid-workspace", false},
		{"workspace-123", false},
		{"my_workspace", false},
		{"", true},
		{"workspace with spaces", true},
		{"workspace@special", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := manager.validateCreate(tt.name)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestManager_Repair(t *testing.T) {
	tests := []struct {
		name            string
		containerExists bool
		wantErr         bool
		errContains     string
	}{
		{"", false, true, "name required"},
		{"missing-ws", false, true, "does not exist"},
		{"missing-container", true, true, "does not exist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockProv := newMockProvider()
			mockProv.exists[tt.name] = tt.containerExists
			manager := NewManager(mockProv)

			err := manager.Repair(tt.name)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestManager_ValidateName(t *testing.T) {
	mockProv := newMockProvider()
	manager := NewManager(mockProv)

	validNames := []string{
		"my-workspace",
		"workspace123",
		"ws_under_score",
		"WS-UPPER",
	}

	invalidNames := []string{
		"",
		"workspace with spaces",
		"workspace@special",
		"workspace!",
	}

	for _, name := range validNames {
		t.Run("valid:"+name, func(t *testing.T) {
			err := manager.validateCreate(name)
			assert.NoError(t, err)
		})
	}

	for _, name := range invalidNames {
		t.Run("invalid:"+name, func(t *testing.T) {
			err := manager.validateCreate(name)
			assert.Error(t, err)
		})
	}
}
