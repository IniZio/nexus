package sync

import (
	"context"
	"github.com/inizio/nexus/packages/nexus/pkg/coordination"
)

type CoordinationStore struct {
	taskManager *coordination.TaskManager
}

func NewCoordinationStore(taskManager *coordination.TaskManager) *CoordinationStore {
	return &CoordinationStore{taskManager: taskManager}
}

func (s *CoordinationStore) SaveSessionID(ctx context.Context, workspaceName, sessionID string) error {
	return s.taskManager.SaveSyncSession(ctx, workspaceName, sessionID)
}

func (s *CoordinationStore) GetSessionID(ctx context.Context, workspaceName string) (string, error) {
	return s.taskManager.GetSyncSession(ctx, workspaceName)
}

func (s *CoordinationStore) DeleteSessionID(ctx context.Context, workspaceName string) error {
	return s.taskManager.DeleteSyncSession(ctx, workspaceName)
}
