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
		if !strings.Contains(parts[1], "/") {
			portStr = parts[1]
		}
		port, _ := strconv.Atoi(portStr)
		return port
	}
	port, _ := strconv.Atoi(portSpec)
	return port
}
