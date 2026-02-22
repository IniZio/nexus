package idle

import (
	"context"
	"sync"
	"time"
)

type Detector struct {
	mu                sync.RWMutex
	workspaceActivity map[string]time.Time
	checkInterval     time.Duration
	idleTimeout       time.Duration
	stopCh            chan struct{}
}

func NewDetector(checkInterval, idleTimeout time.Duration) *Detector {
	return &Detector{
		workspaceActivity: make(map[string]time.Time),
		checkInterval:     checkInterval,
		idleTimeout:       idleTimeout,
		stopCh:            make(chan struct{}),
	}
}

func (d *Detector) Start(ctx context.Context) {
	ticker := time.NewTicker(d.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.checkIdle(ctx)
		}
	}
}

func (d *Detector) Stop() {
	close(d.stopCh)
}

func (d *Detector) RecordActivity(workspaceID string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.workspaceActivity[workspaceID] = time.Now()
}

func (d *Detector) GetIdleDuration(workspaceID string) time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()

	lastActivity, ok := d.workspaceActivity[workspaceID]
	if !ok {
		return d.idleTimeout
	}

	return time.Since(lastActivity)
}

func (d *Detector) IsIdle(workspaceID string) bool {
	return d.GetIdleDuration(workspaceID) >= d.idleTimeout
}

func (d *Detector) SetIdleTimeout(timeout time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.idleTimeout = timeout
}

func (d *Detector) checkIdle(ctx context.Context) {
	d.mu.RLock()
	workspaces := make([]string, 0, len(d.workspaceActivity))
	for id := range d.workspaceActivity {
		workspaces = append(workspaces, id)
	}
	d.mu.RUnlock()

	for _, id := range workspaces {
		if d.IsIdle(id) {
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}
}

type ActivityTracker struct {
	mu           sync.RWMutex
	workspaces   map[string]*WorkspaceActivity
	pollInterval time.Duration
}

type WorkspaceActivity struct {
	LastActive   time.Time
	CPUUsage     float64
	MemoryUsage  int64
	NetworkRx    int64
	NetworkTx    int64
}

func NewActivityTracker(pollInterval time.Duration) *ActivityTracker {
	return &ActivityTracker{
		workspaces:   make(map[string]*WorkspaceActivity),
		pollInterval: pollInterval,
	}
}

func (t *ActivityTracker) Start(ctx context.Context) {
	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.collectMetrics(ctx)
		}
	}
}

func (t *ActivityTracker) RegisterWorkspace(workspaceID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.workspaces[workspaceID] = &WorkspaceActivity{
		LastActive: time.Now(),
	}
}

func (t *ActivityTracker) UnregisterWorkspace(workspaceID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.workspaces, workspaceID)
}

func (t *ActivityTracker) RecordFileActivity(workspaceID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if ws, ok := t.workspaces[workspaceID]; ok {
		ws.LastActive = time.Now()
	}
}

func (t *ActivityTracker) collectMetrics(ctx context.Context) {
}

func (t *ActivityTracker) GetActivity(workspaceID string) *WorkspaceActivity {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.workspaces[workspaceID]
}
