package types

import (
	"testing"
)

func TestBackendTypeString(t *testing.T) {
	tests := []struct {
		backend BackendType
		want    string
	}{
		{BackendDocker, "docker"},
		{BackendSprite, "sprite"},
		{BackendKubernetes, "kubernetes"},
		{BackendDaytona, "daytona"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.backend.String(); got != tt.want {
				t.Errorf("BackendType.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBackendTypeFromString(t *testing.T) {
	tests := []struct {
		str  string
		want BackendType
	}{
		{"docker", BackendDocker},
		{"sprite", BackendSprite},
		{"kubernetes", BackendKubernetes},
		{"daytona", BackendDaytona},
		{"unknown", BackendUnknown},
		{"", BackendUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.str, func(t *testing.T) {
			if got := BackendTypeFromString(tt.str); got != tt.want {
				t.Errorf("BackendTypeFromString(%q) = %v, want %v", tt.str, got, tt.want)
			}
		})
	}
}

func TestDaytonaConfigMarshaling(t *testing.T) {
	config := DaytonaConfig{
		Enabled: true,
		APIURL:  "https://app.daytona.io/api",
	}

	if !config.Enabled {
		t.Error("Expected Enabled to be true")
	}
	if config.APIURL != "https://app.daytona.io/api" {
		t.Errorf("Expected APIURL to be 'https://app.daytona.io/api', got %q", config.APIURL)
	}
}
