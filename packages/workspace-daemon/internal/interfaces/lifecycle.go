package interfaces

type LifecycleManager interface {
	RunPreStart() error
	RunPostStart() error
	RunPreStop() error
	RunPostStop() error
}
