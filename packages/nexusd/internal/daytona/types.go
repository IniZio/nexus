package daytona

import (
	"errors"
	"time"
)

var ErrInvalidAPIKey = errors.New("invalid API key: key cannot be empty")

type CreateSandboxRequest struct {
	Name             string            `json:"name"`
	Image            string            `json:"image,omitempty"`
	Resources        *Resources        `json:"resources,omitempty"`
	EnvVars          map[string]string `json:"env,omitempty"`
	AutoStopInterval int               `json:"autoStopInterval,omitempty"`
}

type Resources struct {
	CPU    int `json:"cpu"`
	Memory int `json:"memory"`
	Disk   int `json:"disk"`
}

type Sandbox struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	State            string            `json:"state"`
	Image            string            `json:"image"`
	Resources        Resources         `json:"resources"`
	EnvVars          map[string]string `json:"env"`
	SSHInfo          SSHInfo           `json:"sshInfo"`
	AutoStopInterval int               `json:"autoStopInterval"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
}

type SSHInfo struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	PrivateKey string `json:"privateKey"`
}

func (s *Sandbox) IsRunning() bool {
	return s.State == "started" || s.State == "running"
}

func (s *Sandbox) IsStopped() bool {
	return s.State == "stopped"
}
