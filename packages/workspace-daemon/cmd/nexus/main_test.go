package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nexus/nexus/packages/workspace-daemon/pkg/compose"
)

func TestParseRequiredPorts(t *testing.T) {
	ports, err := parseRequiredPorts("5173, 5174,5173,8000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []int{5173, 5174, 8000}
	if !reflect.DeepEqual(ports, expected) {
		t.Fatalf("expected %v, got %v", expected, ports)
	}
}

func TestParseRequiredPortsInvalid(t *testing.T) {
	if _, err := parseRequiredPorts("abc"); err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func TestMissingRequiredPorts(t *testing.T) {
	required := []int{5173, 5174, 8000}
	discovered := []compose.PublishedPort{{HostPort: 5173}, {HostPort: 8000}}
	missing := missingRequiredPorts(required, discovered)
	expected := []int{5174}
	if !reflect.DeepEqual(missing, expected) {
		t.Fatalf("expected %v, got %v", expected, missing)
	}
}

func TestParseURLs(t *testing.T) {
	urls := parseURLs("http://a,http://b , http://c")
	expected := []string{"http://a", "http://b", "http://c"}
	if !reflect.DeepEqual(urls, expected) {
		t.Fatalf("expected %v, got %v", expected, urls)
	}
}

func TestAssertNoManualACP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "start.sh")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nset -euo pipefail\ndocker compose up -d\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := assertNoManualACP(dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAssertNoManualACPFindsCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "start-acp.sh")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\nopencode serve --port 4096\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := assertNoManualACP(dir); err == nil {
		t.Fatal("expected error when manual ACP startup command exists")
	}
}
