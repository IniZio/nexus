package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var (
	ErrComposeFileNotFound    = errors.New("docker compose file not found")
	ErrComposeJSONUnsupported = errors.New("docker compose json output unsupported")
)

type PublishedPort struct {
	Service    string `json:"service"`
	HostIP     string `json:"hostIP,omitempty"`
	HostPort   int    `json:"hostPort"`
	TargetPort int    `json:"targetPort"`
	Protocol   string `json:"protocol"`
}

var runComposeCommand = func(ctx context.Context, workspaceRoot string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	cmd.Dir = workspaceRoot
	return cmd.CombinedOutput()
}

func DiscoverPublishedPorts(ctx context.Context, workspaceRoot string) ([]PublishedPort, error) {
	composeFile, found, err := findComposeFile(workspaceRoot)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrComposeFileNotFound
	}

	out, err := runComposeCommand(ctx, workspaceRoot, "-f", composeFile, "config", "--format", "json")
	if err != nil {
		_, _ = runComposeCommand(ctx, workspaceRoot, "-f", composeFile, "config")
		return nil, fmt.Errorf("%w: %v", ErrComposeJSONUnsupported, err)
	}

	ports, err := parsePublishedPortsFromConfigJSON(out)
	if err != nil {
		return nil, err
	}

	return ports, nil
}

func findComposeFile(workspaceRoot string) (string, bool, error) {
	for _, name := range []string{"docker-compose.yml", "docker-compose.yaml"} {
		path := filepath.Join(workspaceRoot, name)
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return path, true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
	}
	return "", false, nil
}

func parsePublishedPortsFromConfigJSON(data []byte) ([]PublishedPort, error) {
	type composeService struct {
		Ports []json.RawMessage `json:"ports"`
	}
	type composeConfig struct {
		Services map[string]composeService `json:"services"`
	}

	var cfg composeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse compose config json: %w", err)
	}

	result := make([]PublishedPort, 0)
	for service, svc := range cfg.Services {
		for _, raw := range svc.Ports {
			if p, ok := parseObjectPortMapping(service, raw); ok {
				result = append(result, p)
				continue
			}
			if p, ok := parseStringPortMapping(service, raw); ok {
				result = append(result, p)
			}
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Service != result[j].Service {
			return result[i].Service < result[j].Service
		}
		if result[i].HostPort != result[j].HostPort {
			return result[i].HostPort < result[j].HostPort
		}
		return result[i].TargetPort < result[j].TargetPort
	})

	return result, nil
}

func parseObjectPortMapping(service string, raw json.RawMessage) (PublishedPort, bool) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || len(obj) == 0 {
		return PublishedPort{}, false
	}

	published, ok := asInt(obj["published"])
	if !ok || published <= 0 {
		return PublishedPort{}, false
	}

	target, ok := asInt(obj["target"])
	if !ok || target <= 0 {
		return PublishedPort{}, false
	}

	protocol := asString(obj["protocol"])
	if protocol == "" {
		protocol = "tcp"
	}

	hostIP := asString(obj["host_ip"])
	if hostIP == "" {
		hostIP = asString(obj["hostIP"])
	}

	return PublishedPort{
		Service:    service,
		HostIP:     hostIP,
		HostPort:   published,
		TargetPort: target,
		Protocol:   protocol,
	}, true
}

func parseStringPortMapping(service string, raw json.RawMessage) (PublishedPort, bool) {
	var spec string
	if err := json.Unmarshal(raw, &spec); err != nil || spec == "" {
		return PublishedPort{}, false
	}

	protocol := "tcp"
	if strings.Contains(spec, "/") {
		parts := strings.SplitN(spec, "/", 2)
		spec = parts[0]
		if parts[1] != "" {
			protocol = parts[1]
		}
	}

	parts := strings.Split(spec, ":")
	var hostIP string
	var publishedStr string
	var targetStr string

	switch len(parts) {
	case 2:
		publishedStr = parts[0]
		targetStr = parts[1]
	case 3:
		hostIP = parts[0]
		publishedStr = parts[1]
		targetStr = parts[2]
	default:
		return PublishedPort{}, false
	}

	published, err := strconv.Atoi(publishedStr)
	if err != nil || published <= 0 {
		return PublishedPort{}, false
	}
	target, err := strconv.Atoi(targetStr)
	if err != nil || target <= 0 {
		return PublishedPort{}, false
	}

	return PublishedPort{
		Service:    service,
		HostIP:     hostIP,
		HostPort:   published,
		TargetPort: target,
		Protocol:   protocol,
	}, true
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case string:
		i, err := strconv.Atoi(n)
		if err != nil {
			return 0, false
		}
		return i, true
	default:
		return 0, false
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}
