package mocks

import "errors"

type MockPortManager struct {
	Allocated   map[int32]bool
	AllocateErr error
	ReleaseErr  error
	NextPort    int32
	MinPort     int32
	MaxPort     int32
}

func NewMockPortManager() *MockPortManager {
	return &MockPortManager{
		Allocated: make(map[int32]bool),
		NextPort:  32800,
		MinPort:   32800,
		MaxPort:   34999,
	}
}

func (m *MockPortManager) Allocate() (int32, error) {
	if m.AllocateErr != nil {
		return 0, m.AllocateErr
	}
	port := m.NextPort
	m.NextPort++
	m.Allocated[port] = true
	return port, nil
}

func (m *MockPortManager) AllocateSpecific(port int32) error {
	if port < m.MinPort || port > m.MaxPort {
		return errors.New("port out of range")
	}
	if m.Allocated[port] {
		return errors.New("port already allocated")
	}
	m.Allocated[port] = true
	return nil
}

func (m *MockPortManager) Release(port int32) error {
	if m.ReleaseErr != nil {
		return m.ReleaseErr
	}
	if !m.Allocated[port] {
		return errors.New("port not allocated")
	}
	delete(m.Allocated, port)
	return nil
}

func (m *MockPortManager) IsAllocated(port int32) bool {
	return m.Allocated[port]
}

func (m *MockPortManager) GetAllocatedPorts() []int32 {
	ports := make([]int32, 0, len(m.Allocated))
	for port := range m.Allocated {
		ports = append(ports, port)
	}
	return ports
}
