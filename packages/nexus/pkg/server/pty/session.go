package pty

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

type Session struct {
	ID         string
	Cmd        *exec.Cmd
	File       *os.File
	RemoteConn net.Conn
	Mu         sync.Mutex
	Closing    atomic.Bool
	Enc        *json.Encoder
	Dec        *json.Decoder
	Remote     bool
	Done       chan struct{}
}
