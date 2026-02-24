package idle

import (
	"context"
	"sync"
	"time"

	"github.com/nexus/nexus/packages/nexusd/internal/config"
)

type ActivityType string

const (
	ActivitySSH    ActivityType = "ssh"
	ActivityFile   ActivityType = "file"
	ActivityHTTP   ActivityType = "http"
	ActivityResume ActivityType = "resume"
)

type IdleDetector struct {
	mu           sync.RWMutex
	workspaceID  string
	threshold    time.Duration
	lastActivity time.Time
	activityChan chan ActivityType
	stopChan     chan struct{}
	isRunning    bool
	onIdle       func()
	onActive     func()
}

func NewIdleDetector(workspaceID string, threshold time.Duration) *IdleDetector {
	return &IdleDetector{
		workspaceID:  workspaceID,
		threshold:    threshold,
		lastActivity: time.Now(),
		activityChan: make(chan ActivityType, 10),
		stopChan:     make(chan struct{}),
		isRunning:    false,
	}
}

func (d *IdleDetector) Start() {
	d.mu.Lock()
	if d.isRunning {
		d.mu.Unlock()
		return
	}
	d.isRunning = true
	d.mu.Unlock()

	go d.monitorLoop()
}

func (d *IdleDetector) Stop() {
	select {
	case <-d.stopChan:
		return
	default:
		close(d.stopChan)
	}

	d.mu.Lock()
	d.isRunning = false
	d.mu.Unlock()
}

func (d *IdleDetector) monitorLoop() {
	ticker := time.NewTicker(config.DefaultIdleTimeout)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.mu.RLock()
			isIdle := time.Since(d.lastActivity) >= d.threshold
			onIdle := d.onIdle
			d.mu.RUnlock()

			if isIdle && onIdle != nil {
				onIdle()
			}
		case activity := <-d.activityChan:
			d.mu.Lock()
			d.lastActivity = time.Now()
			onActive := d.onActive
			d.mu.Unlock()

			if activity == ActivityResume && onActive != nil {
				onActive()
			}
		case <-d.stopChan:
			return
		}
	}
}

func (d *IdleDetector) RecordActivity(activity ActivityType) {
	select {
	case d.activityChan <- activity:
	default:
	}
}

func (d *IdleDetector) IsIdle() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return time.Since(d.lastActivity) >= d.threshold
}

func (d *IdleDetector) GetIdleDuration() time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()

	return time.Since(d.lastActivity)
}

func (d *IdleDetector) SetThreshold(threshold time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.threshold = threshold
}

func (d *IdleDetector) SetOnIdle(callback func()) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.onIdle = callback
}

func (d *IdleDetector) SetOnActive(callback func()) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.onActive = callback
}

type ActivityTracker struct {
	mu           sync.RWMutex
	workspaces   map[string]*WorkspaceActivity
	pollInterval time.Duration
}

type WorkspaceActivity struct {
	LastActive  time.Time
	CPUUsage    float64
	MemoryUsage int64
	NetworkRx   int64
	NetworkTx   int64
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
	_ = ctx
}

func (t *ActivityTracker) GetActivity(workspaceID string) *WorkspaceActivity {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.workspaces[workspaceID]
}
