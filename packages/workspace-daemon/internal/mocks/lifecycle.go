package mocks

type MockLifecycleManager struct {
	PreStartErr  error
	PostStartErr error
	PreStopErr   error
	PostStopErr  error
}

func NewMockLifecycleManager() *MockLifecycleManager {
	return &MockLifecycleManager{}
}

func (m *MockLifecycleManager) RunPreStart() error {
	return m.PreStartErr
}

func (m *MockLifecycleManager) RunPostStart() error {
	return m.PostStartErr
}

func (m *MockLifecycleManager) RunPreStop() error {
	return m.PreStopErr
}

func (m *MockLifecycleManager) RunPostStop() error {
	return m.PostStopErr
}
