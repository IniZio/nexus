package main

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestProbeFirecrackerToolingChecksFirecrackerBinary(t *testing.T) {
	lookup := func(name string) (string, error) {
		if name == "firecracker" {
			return "/usr/bin/firecracker", nil
		}
		return "", errors.New("not found")
	}
	if !probeFirecrackerTooling(lookup) {
		t.Fatal("expected true when firecracker binary exists")
	}
}

func TestProbeFirecrackerToolingReturnsFalseWhenBinaryMissing(t *testing.T) {
	lookup := func(name string) (string, error) {
		return "", errors.New("not found")
	}
	if probeFirecrackerTooling(lookup) {
		t.Fatal("expected false when firecracker binary is missing")
	}
}

func TestProbeFirecrackerToolingUsesLookPathByDefault(t *testing.T) {
	// Test that the default behavior uses exec.LookPath
	// by checking if firecracker is in PATH
	_, err := exec.LookPath("firecracker")
	expected := err == nil
	
	result := probeFirecrackerTooling(exec.LookPath)
	if result != expected {
		t.Fatalf("expected %v when using exec.LookPath, got %v", expected, result)
	}
}

func TestProbeFirecrackerToolingIntegration(t *testing.T) {
	// Create a temporary directory with a fake firecracker binary
	tmpDir := t.TempDir()
	fakeFc := tmpDir + "/firecracker"
	
	// Create fake binary
	if err := os.WriteFile(fakeFc, []byte("#!/bin/bash\necho 'fake'"), 0755); err != nil {
		t.Fatalf("failed to create fake binary: %v", err)
	}
	
	// Test with lookup that finds our fake binary
	lookup := func(name string) (string, error) {
		if name == "firecracker" {
			return fakeFc, nil
		}
		return "", errors.New("not found")
	}
	
	if !probeFirecrackerTooling(lookup) {
		t.Fatal("expected true when firecracker binary is found")
	}
}