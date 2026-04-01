package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverPublishedPorts_NoComposeFile(t *testing.T) {
	root := t.TempDir()

	_, err := DiscoverPublishedPorts(context.Background(), root)
	if !errors.Is(err, ErrComposeFileNotFound) {
		t.Fatalf("expected ErrComposeFileNotFound, got %v", err)
	}
}

func TestDiscoverPublishedPorts_DetectsYMLAndParsesPublishedPorts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), []byte("services:{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := runComposeCommand
	t.Cleanup(func() { runComposeCommand = orig })
	runComposeCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[len(args)-2] == "--format" && args[len(args)-1] == "json" {
			return []byte(`{
  "services": {
    "student": {
      "ports": [
        {"target": 5173, "published": 5173, "protocol": "tcp", "host_ip": "127.0.0.1"},
        "6006:6006"
      ]
    },
    "api": {
      "ports": ["127.0.0.1:8000:8000/tcp"]
    }
  }
}`), nil
		}
		return []byte(""), nil
	}

	ports, err := DiscoverPublishedPorts(context.Background(), root)
	if err != nil {
		t.Fatalf("discover ports: %v", err)
	}

	if len(ports) != 3 {
		t.Fatalf("expected 3 ports, got %d", len(ports))
	}

	if ports[0].Service != "api" || ports[0].HostPort != 8000 || ports[0].TargetPort != 8000 {
		t.Fatalf("unexpected first mapping: %+v", ports[0])
	}
	if ports[1].Service != "student" || ports[1].HostPort != 5173 || ports[1].TargetPort != 5173 {
		t.Fatalf("unexpected second mapping: %+v", ports[1])
	}
	if ports[2].Service != "student" || ports[2].HostPort != 6006 || ports[2].TargetPort != 6006 {
		t.Fatalf("unexpected third mapping: %+v", ports[2])
	}
}

func TestDiscoverPublishedPorts_DetectsYAMLComposeFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yaml"), []byte("services:{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := runComposeCommand
	t.Cleanup(func() { runComposeCommand = orig })
	runComposeCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) > 2 && args[0] == "-f" && filepath.Base(args[1]) != "docker-compose.yaml" {
			t.Fatalf("expected docker-compose.yaml to be selected, got args=%v", args)
		}
		return []byte(`{"services":{"web":{"ports":["5173:5173"]}}}`), nil
	}

	ports, err := DiscoverPublishedPorts(context.Background(), root)
	if err != nil {
		t.Fatalf("discover ports: %v", err)
	}
	if len(ports) != 1 || ports[0].HostPort != 5173 || ports[0].TargetPort != 5173 {
		t.Fatalf("unexpected ports: %+v", ports)
	}
}

func TestDiscoverPublishedPorts_ReturnsComposeJSONUnsupportedOnFormatFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "docker-compose.yml"), []byte("services:{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := runComposeCommand
	t.Cleanup(func() { runComposeCommand = orig })
	runComposeCommand = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) >= 4 && args[len(args)-2] == "--format" && args[len(args)-1] == "json" {
			return []byte(""), errors.New("unknown flag: --format")
		}
		return []byte("services:\n  web:\n    ports:\n      - \"5173:5173\"\n"), nil
	}

	_, err := DiscoverPublishedPorts(context.Background(), root)
	if !errors.Is(err, ErrComposeJSONUnsupported) {
		t.Fatalf("expected ErrComposeJSONUnsupported, got %v", err)
	}
}
