package idle

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestIdleDetector_IsIdle(t *testing.T) {
	d := NewIdleDetector("test-ws", 100*time.Millisecond)

	assert.False(t, d.IsIdle())

	d.RecordActivity(ActivitySSH)
	assert.False(t, d.IsIdle())

	d.mu.Lock()
	d.lastActivity = time.Now().Add(-200 * time.Millisecond)
	d.mu.Unlock()

	assert.True(t, d.IsIdle())
}

func TestIdleDetector_RecordActivity(t *testing.T) {
	d := NewIdleDetector("test-ws", 50*time.Millisecond)

	initialLastActivity := d.lastActivity

	d.RecordActivity(ActivitySSH)

	d.mu.RLock()
	assert.True(t, d.lastActivity.After(initialLastActivity) || d.lastActivity.Equal(initialLastActivity))
	d.mu.RUnlock()
}

func TestIdleDetector_GetIdleDuration(t *testing.T) {
	d := NewIdleDetector("test-ws", 100*time.Millisecond)

	duration := d.GetIdleDuration()
	assert.LessOrEqual(t, duration, 50*time.Millisecond)

	d.RecordActivity(ActivityFile)

	duration = d.GetIdleDuration()
	assert.LessOrEqual(t, duration, 50*time.Millisecond)
}

func TestIdleDetector_SetThreshold(t *testing.T) {
	d := NewIdleDetector("test-ws", 100*time.Millisecond)

	d.SetThreshold(200 * time.Millisecond)

	d.mu.RLock()
	assert.Equal(t, 200*time.Millisecond, d.threshold)
	d.mu.RUnlock()
}

func TestIdleDetector_SetOnIdle(t *testing.T) {
	d := NewIdleDetector("test-ws", 50*time.Millisecond)

	called := false
	d.SetOnIdle(func() {
		called = true
	})

	d.mu.RLock()
	onIdle := d.onIdle
	d.mu.RUnlock()

	assert.NotNil(t, onIdle)
	assert.False(t, called)
}

func TestIdleDetector_SetOnActive(t *testing.T) {
	d := NewIdleDetector("test-ws", 50*time.Millisecond)

	called := false
	d.SetOnActive(func() {
		called = true
	})

	d.mu.RLock()
	onActive := d.onActive
	d.mu.RUnlock()

	assert.NotNil(t, onActive)
	assert.False(t, called)
}

func TestIdleDetector_StartStop(t *testing.T) {
	d := NewIdleDetector("test-ws", 50*time.Millisecond)

	d.Start()

	d.mu.RLock()
	assert.True(t, d.isRunning)
	d.mu.RUnlock()

	d.Stop()

	d.mu.RLock()
	assert.False(t, d.isRunning)
	d.mu.RUnlock()
}

func TestIdleDetector_StartTwice(t *testing.T) {
	d := NewIdleDetector("test-ws", 50*time.Millisecond)

	d.Start()
	d.Start()

	d.mu.RLock()
	isRunning := d.isRunning
	d.mu.RUnlock()

	assert.True(t, isRunning)

	d.Stop()
}

func TestActivityTracker_RegisterWorkspace(t *testing.T) {
	tracker := NewActivityTracker(10 * time.Second)

	tracker.RegisterWorkspace("ws-1")

	activity := tracker.GetActivity("ws-1")
	assert.NotNil(t, activity)
}

func TestActivityTracker_UnregisterWorkspace(t *testing.T) {
	tracker := NewActivityTracker(10 * time.Second)

	tracker.RegisterWorkspace("ws-1")
	tracker.UnregisterWorkspace("ws-1")

	activity := tracker.GetActivity("ws-1")
	assert.Nil(t, activity)
}

func TestActivityTracker_RecordFileActivity(t *testing.T) {
	tracker := NewActivityTracker(10 * time.Second)

	tracker.RegisterWorkspace("ws-1")
	initialActive := tracker.GetActivity("ws-1").LastActive

	time.Sleep(10 * time.Millisecond)

	tracker.RecordFileActivity("ws-1")
	updatedActive := tracker.GetActivity("ws-1").LastActive

	assert.True(t, updatedActive.After(initialActive) || updatedActive.Equal(initialActive))
}

func TestActivityType_Values(t *testing.T) {
	assert.Equal(t, ActivityType("ssh"), ActivitySSH)
	assert.Equal(t, ActivityType("file"), ActivityFile)
	assert.Equal(t, ActivityType("http"), ActivityHTTP)
	assert.Equal(t, ActivityType("resume"), ActivityResume)
}

func TestIdleDetector_MultipleActivities(t *testing.T) {
	d := NewIdleDetector("test-ws", 100*time.Millisecond)

	d.RecordActivity(ActivitySSH)
	d.RecordActivity(ActivityFile)
	d.RecordActivity(ActivityHTTP)

	d.mu.RLock()
	assert.True(t, d.lastActivity.After(time.Now().Add(-50*time.Millisecond)))
	d.mu.RUnlock()
}

func TestIdleDetector_ResumeActivity(t *testing.T) {
	d := NewIdleDetector("test-ws", 50*time.Millisecond)

	activeCalled := atomic.Bool{}
	d.SetOnActive(func() {
		activeCalled.Store(true)
	})

	d.Start()
	defer d.Stop()

	d.RecordActivity(ActivityResume)

	time.Sleep(20 * time.Millisecond)

	assert.True(t, activeCalled.Load())
}

func TestIdleDetector_StopTwice(t *testing.T) {
	d := NewIdleDetector("test-ws", 50*time.Millisecond)

	d.Start()
	d.Stop()
	d.Stop()
}

func TestActivityTracker_StartCancel(t *testing.T) {
	tracker := NewActivityTracker(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	go tracker.Start(ctx)

	tracker.RegisterWorkspace("ws-1")

	time.Sleep(20 * time.Millisecond)

	cancel()

	time.Sleep(20 * time.Millisecond)
}

func TestActivityTracker_GetActivityNotFound(t *testing.T) {
	tracker := NewActivityTracker(10 * time.Second)

	activity := tracker.GetActivity("non-existent")

	assert.Nil(t, activity)
}

func TestIdleDetector_ConcurrentActivity(t *testing.T) {
	d := NewIdleDetector("test-ws", 100*time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.RecordActivity(ActivitySSH)
		}()
	}

	wg.Wait()

	d.mu.RLock()
	assert.True(t, d.lastActivity.After(time.Now().Add(-50*time.Millisecond)))
	d.mu.RUnlock()
}

func TestIdleDetector_ConcurrentStartStop(t *testing.T) {
	d := NewIdleDetector("test-ws", 50*time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Start()
			time.Sleep(10 * time.Millisecond)
		}()
	}

	wg.Wait()
	d.Stop()
}

func TestIdleDetector_ActivityChannelFull(t *testing.T) {
	d := NewIdleDetector("test-ws", 50*time.Millisecond)

	for i := 0; i < 20; i++ {
		d.RecordActivity(ActivitySSH)
	}
}

func TestIdleDetector_IsIdleAtThreshold(t *testing.T) {
	d := NewIdleDetector("test-ws", 100*time.Millisecond)

	d.mu.Lock()
	d.lastActivity = time.Now().Add(-100 * time.Millisecond)
	d.mu.Unlock()

	assert.True(t, d.IsIdle())
}

func TestIdleDetector_IsIdleJustBelowThreshold(t *testing.T) {
	d := NewIdleDetector("test-ws", 100*time.Millisecond)

	d.mu.Lock()
	d.lastActivity = time.Now().Add(-99 * time.Millisecond)
	d.mu.Unlock()

	assert.False(t, d.IsIdle())
}

func TestActivityTracker_CollectMetrics(t *testing.T) {
	tracker := NewActivityTracker(10 * time.Millisecond)

	tracker.RegisterWorkspace("ws-1")

	tracker.collectMetrics(context.Background())
}
