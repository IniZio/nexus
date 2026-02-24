package docker

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
)

type PortManager struct {
	mu        sync.RWMutex
	allocated map[int32]bool
	nextPort  int32
	minPort   int32
	maxPort   int32
	stateFile string
}

type PortState struct {
	Allocated map[int32]bool `json:"allocated"`
	NextPort  int32          `json:"nextPort"`
}

func NewPortManager(minPort, maxPort int32) *PortManager {
	return &PortManager{
		allocated: make(map[int32]bool),
		nextPort:  minPort,
		minPort:   minPort,
		maxPort:   maxPort,
	}
}

func NewPortManagerWithState(minPort, maxPort int32, stateFile string) *PortManager {
	pm := &PortManager{
		allocated: make(map[int32]bool),
		nextPort:  minPort,
		minPort:   minPort,
		maxPort:   maxPort,
		stateFile: stateFile,
	}

	if stateFile != "" {
		pm.loadState()
	}

	return pm
}

func isPortInUse(port int32) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func (p *PortManager) Allocate() (int32, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := int32(0); i <= p.maxPort-p.minPort; i++ {
		port := p.nextPort
		p.nextPort++
		if p.nextPort > p.maxPort {
			p.nextPort = p.minPort
		}

		if !p.allocated[port] && !isPortInUse(port) {
			p.allocated[port] = true
			p.saveState()
			return port, nil
		}
	}

	return 0, fmt.Errorf("no available ports in range %d-%d", p.minPort, p.maxPort)
}

func (p *PortManager) AllocateSpecific(port int32) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if port < p.minPort || port > p.maxPort {
		return fmt.Errorf("port %d out of range (%d-%d)", port, p.minPort, p.maxPort)
	}

	if p.allocated[port] {
		return fmt.Errorf("port %d already allocated", port)
	}

	p.allocated[port] = true
	p.saveState()
	return nil
}

func (p *PortManager) Release(port int32) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.allocated[port] {
		return fmt.Errorf("port %d not allocated", port)
	}

	delete(p.allocated, port)
	p.saveState()
	return nil
}

func (p *PortManager) IsAllocated(port int32) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.allocated[port]
}

func (p *PortManager) GetAllocatedPorts() []int32 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	ports := make([]int32, 0, len(p.allocated))
	for port := range p.allocated {
		ports = append(ports, port)
	}

	return ports
}

func (p *PortManager) saveState() {
	if p.stateFile == "" {
		return
	}

	state := PortState{
		Allocated: p.allocated,
		NextPort:  p.nextPort,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("[ERROR] Failed to marshal port state: %v", err)
		return
	}

	if err := os.WriteFile(p.stateFile, data, 0644); err != nil {
		log.Printf("[ERROR] Failed to write port state: %v", err)
	}
}

func (p *PortManager) loadState() {
	if p.stateFile == "" {
		return
	}

	data, err := os.ReadFile(p.stateFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[ERROR] Failed to read port state: %v", err)
		}
		return
	}

	var state PortState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("[ERROR] Failed to unmarshal port state: %v", err)
		return
	}

	p.allocated = state.Allocated
	if state.NextPort > 0 {
		p.nextPort = state.NextPort
	}
}

func (p *PortManager) GetState() PortState {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return PortState{
		Allocated: p.allocated,
		NextPort:  p.nextPort,
	}
}

func (p *PortManager) Restore(ports []int32) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, port := range ports {
		if port < p.minPort || port > p.maxPort {
			return fmt.Errorf("port %d out of range (%d-%d)", port, p.minPort, p.maxPort)
		}
		p.allocated[port] = true
	}

	p.saveState()
	return nil
}
