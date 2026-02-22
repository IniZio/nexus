package interfaces

type PortManager interface {
	Allocate() (int32, error)
	AllocateSpecific(port int32) error
	Release(port int32) error
	IsAllocated(port int32) bool
	GetAllocatedPorts() []int32
}
