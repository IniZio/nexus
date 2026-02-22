# Port Forwarding with Docker-Compose Auto-Detection Implementation Plan

**Goal:** Complete port forwarding implementation with docker-compose.yml auto-detection

**Architecture:** Parse docker-compose.yml to extract service ports, auto-forward them when workspace starts with --dind, detect running container ports and forward them

**Tech Stack:** Go, Docker API, gopkg.in/yaml.v3

---

### Task 1: Create compose parser

**Files:**
- Create: `packages/workspace-daemon/internal/docker/compose.go`

**Step 1: Write compose parser**

```go
package docker

import (
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type ComposeFile struct {
	Services map[string]ComposeService `yaml:"services"`
}

type ComposeService struct {
	Image string   `yaml:"image"`
	Ports []string `yaml:"ports"`
}

func ParseComposeFile(path string) ([]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var compose ComposeFile
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil, err
	}

	ports := []int{}
	for _, service := range compose.Services {
		for _, port := range service.Ports {
			containerPort := parsePort(port)
			if containerPort > 0 {
				ports = append(ports, containerPort)
			}
		}
	}

	return ports, nil
}

func parsePort(portSpec string) int {
	parts := strings.Split(portSpec, ":")
	if len(parts) >= 2 {
		portStr := parts[0]
		if strings.Contains(parts[1], "/") {
			portStr = parts[0]
		} else {
			portStr = parts[1]
		}
		port, _ := strconv.Atoi(portStr)
		return port
	}
	port, _ := strconv.Atoi(portSpec)
	return port
}
```

**Step 2: Add gopkg.in/yaml.v3 to go.mod if needed**

Run: `cd packages/workspace-daemon && go get gopkg.in/yaml.v3`

---

### Task 2: Enhance backend to auto-forward ports

**Files:**
- Modify: `packages/workspace-daemon/internal/docker/backend.go`

**Step 1: Modify CreateWorkspace to parse compose and forward ports**

In CreateWorkspace function, after line ~115 (where worktreePath is handled), add compose parsing:

```go
// Parse docker-compose.yml for auto-port forwarding
var extraPorts []PortBinding
if req.DinD && req.WorktreePath != "" {
	composePath := filepath.Join(req.WorktreePath, "docker-compose.yml")
	if _, err := os.Stat(composePath); err == nil {
		ports, err := ParseComposeFile(composePath)
		if err != nil {
			fmt.Printf("Warning: failed to parse compose file: %v\n", err)
		} else {
			fmt.Printf("Found %d ports in docker-compose.yml\n", len(ports))
			for _, port := range ports {
				hostPort, err := b.portManager.Allocate()
				if err != nil {
					fmt.Printf("Warning: failed to allocate port %d: %v\n", port, err)
					continue
				}
				extraPorts = append(extraPorts, PortBinding{
					ContainerPort: int32(port),
					HostPort:      hostPort,
					Protocol:      "tcp",
				})
			}
		}
	}
}
```

Then modify line ~142 to include extraPorts:

```go
Ports: append([]PortBinding{{ContainerPort: 22, HostPort: sshPort, Protocol: "tcp"}}, extraPorts...),
```

And modify workspace.Ports to include the extra ports:

```go
workspace.Ports = []wsTypes.PortMapping{
	{
		Name:          "ssh",
		Protocol:      "tcp",
		ContainerPort: 22,
		HostPort:      sshPort,
		Visibility:    "public",
	},
}
for _, p := range extraPorts {
	workspace.Ports = append(workspace.Ports, wsTypes.PortMapping{
		Name:          fmt.Sprintf("port-%d", p.ContainerPort),
		Protocol:      p.Protocol,
		ContainerPort: p.ContainerPort,
		HostPort:      p.HostPort,
		Visibility:    "public",
		URL:           fmt.Sprintf("http://localhost:%d", p.HostPort),
	})
}
```

Also add "path/filepath" to imports.

---

### Task 3: Fix server to include ports in workspace state

**Files:**
- Modify: `packages/workspace-daemon/pkg/server/server.go`

**Step 1: Update createWorkspace to include ports from docker backend**

Around line 599-605, after docker workspace is created:

```go
createdWS, err := s.dockerBackend.CreateWorkspaceWithBridge(r.Context(), dockerReq, bridgeSocketPath)
if err != nil {
	WriteError(w, http.StatusInternalServerError, fmt.Errorf("creating docker workspace: %w", err))
	return
}
wsID = createdWS.ID

// Copy ports from docker workspace to server workspace state
ws.Ports = make([]PortMapping, len(createdWS.Ports))
for i, p := range createdWS.Ports {
	ws.Ports[i] = PortMapping{
		Name:          p.Name,
		Protocol:      p.Protocol,
		ContainerPort: int(p.ContainerPort),
		HostPort:      int(p.HostPort),
		Visibility:    p.Visibility,
		URL:           p.URL,
	}
}
```

---

### Task 4: Verify build and test

**Step 1: Build the workspace-daemon**

Run: `cd packages/workspace-daemon && go build ./...`

**Step 2: Run tests**

Run: `cd packages/workspace-daemon && go test ./...`

---

### Task 5: Commit changes

```bash
git add packages/workspace-daemon/internal/docker/compose.go
git add packages/workspace-daemon/internal/docker/backend.go
git add packages/workspace-daemon/pkg/server/server.go
git commit -m "feat: complete port forwarding with docker-compose auto-detection

- Parse docker-compose.yml for service ports
- Auto-forward all exposed ports on workspace creation
- Store port allocations in workspace state
- nexus url command shows accessible URLs
- Support for multiple service ports
- Works with DinD and docker-compose"
```
