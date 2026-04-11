package main

import (
	"strings"
	"testing"
)

func TestErrNotAbsProjectRoot(t *testing.T) {
	err := errNotAbsProjectRoot("project root", ".")
	if err == nil {
		t.Fatal("expected error")
	}
	s := err.Error()
	if !strings.Contains(s, "try:") || !strings.Contains(s, "--project-root") {
		t.Fatalf("expected hint with --project-root, got: %v", err)
	}
}
