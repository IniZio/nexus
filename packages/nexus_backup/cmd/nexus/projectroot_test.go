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
	if !strings.Contains(s, "resolved:") {
		t.Fatalf("expected resolved absolute path hint, got: %v", err)
	}
}
