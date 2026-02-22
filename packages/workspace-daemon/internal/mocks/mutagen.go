package mocks

import "context"

type MockMutagenClient struct {
	StartErr error
	StopErr  error
}

func NewMockMutagenClient() *MockMutagenClient {
	return &MockMutagenClient{}
}

func (m *MockMutagenClient) Start(ctx context.Context) error {
	return m.StartErr
}

func (m *MockMutagenClient) Stop(ctx context.Context) error {
	return m.StopErr
}
