package ssh

import (
	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

type ConnectionConfig struct {
	Host                  string
	Port                  int
	Username              string
	PrivateKey            string
	StrictHostKeyChecking bool
	ServerAliveInterval   int
	ConnectTimeout        int
}

func GetDaytonaSSHConfig(conn *types.SSHConnection) *ConnectionConfig {
	return &ConnectionConfig{
		Host:                  conn.Host,
		Port:                  int(conn.Port),
		Username:              conn.Username,
		PrivateKey:            conn.PrivateKey,
		StrictHostKeyChecking: false,
		ServerAliveInterval:   30,
		ConnectTimeout:        10,
	}
}
