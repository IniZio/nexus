package transport

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type MockTransportFactory struct {
	createCount int
}

func (f *MockTransportFactory) CreateTransport(config *Config) (Transport, error) {
	f.createCount++
	return &MockTransport{}, nil
}

func TestPoolCreation(t *testing.T) {
	config := &Config{
		Connection: ConnectionConfig{
			MaxConns:    5,
			MaxIdle:     2,
			MaxLifetime: 1 * time.Hour,
			IdleTimeout: 30 * time.Minute,
		},
	}

	factory := &MockTransportFactory{}
	pool := NewPool(config, "mock", factory)

	assert.NotNil(t, pool)
	assert.Equal(t, config, pool.config)
	assert.Equal(t, "mock", pool.transportType)
	assert.Equal(t, factory, pool.factory)
	assert.NotNil(t, pool.conns)
	assert.NotNil(t, pool.metrics)
}

func TestPoolGet(t *testing.T) {
	config := &Config{
		Connection: ConnectionConfig{
			MaxConns: 2,
			MaxIdle:  1,
		},
	}

	factory := &MockTransportFactory{}
	pool := NewPool(config, "mock", factory)

	ctx := context.Background()

	// Get first connection
	transport1, err := pool.Get(ctx, "test://target1")
	require.NoError(t, err)
	require.NotNil(t, transport1)
	assert.Equal(t, 1, factory.createCount)
	err = transport1.Connect(ctx, "test://target1")
	require.NoError(t, err)

	// Get second connection to different target
	transport2, err := pool.Get(ctx, "test://target2")
	require.NoError(t, err)
	require.NotNil(t, transport2)
	assert.Equal(t, 2, factory.createCount)
	err = transport2.Connect(ctx, "test://target2")
	require.NoError(t, err)

	// Return connections to pool
	err = transport2.Disconnect(ctx)
	require.NoError(t, err)
	err = transport1.Disconnect(ctx)
	require.NoError(t, err)

	// Get connection again (should reuse)
	transport3, err := pool.Get(ctx, "test://target1")
	require.NoError(t, err)
	require.NotNil(t, transport3)
	assert.Equal(t, 2, factory.createCount) // Should not create new one
}

func TestPoolExhausted(t *testing.T) {
	config := &Config{
		Connection: ConnectionConfig{
			MaxConns: 1,
		},
	}

	factory := &MockTransportFactory{}
	pool := NewPool(config, "mock", factory)

	ctx := context.Background()

	// Get first connection
	transport1, err := pool.Get(ctx, "test://target")
	require.NoError(t, err)
	require.NotNil(t, transport1)

	// Try to get second connection (should fail)
	transport2, err := pool.Get(ctx, "test://target")
	assert.Error(t, err)
	assert.Nil(t, transport2)
	assert.Contains(t, err.Error(), "connection pool exhausted")
}

func TestPoolMetrics(t *testing.T) {
	config := &Config{
		Connection: ConnectionConfig{
			MaxConns: 2,
			MaxIdle:  1,
		},
	}

	factory := &MockTransportFactory{}
	pool := NewPool(config, "mock", factory)

	ctx := context.Background()

	// Initial metrics
	metrics := pool.GetMetrics()
	assert.Equal(t, 0, metrics.Created)
	assert.Equal(t, 0, metrics.Destroyed)
	assert.Equal(t, 0, metrics.Active)
	assert.Equal(t, 0, metrics.Idle)

	// Get connections
	transport1, _ := pool.Get(ctx, "test://target1")
	transport2, _ := pool.Get(ctx, "test://target2")

	metrics = pool.GetMetrics()
	assert.Equal(t, 2, metrics.Created)
	assert.Equal(t, 2, metrics.Active)
	assert.Equal(t, 0, metrics.Idle)

	// Return connections
	transport1.Disconnect(ctx)
	transport2.Disconnect(ctx)

	metrics = pool.GetMetrics()
	assert.Equal(t, 2, metrics.Created)
	assert.Equal(t, 0, metrics.Active)
	assert.Equal(t, 2, metrics.Idle)
}

func TestPoolClose(t *testing.T) {
	config := &Config{
		Connection: ConnectionConfig{
			MaxConns: 2,
		},
	}

	factory := &MockTransportFactory{}
	pool := NewPool(config, "mock", factory)

	ctx := context.Background()

	// Get connections
	transport1, _ := pool.Get(ctx, "test://target1")
	transport2, _ := pool.Get(ctx, "test://target2")

	// Return to pool
	transport1.Disconnect(ctx)
	transport2.Disconnect(ctx)

	// Close pool
	err := pool.Close()
	require.NoError(t, err)

	// Verify metrics
	metrics := pool.GetMetrics()
	assert.Equal(t, 2, metrics.Created)
	assert.Greater(t, metrics.Destroyed, 0)
}

func TestPooledConnection(t *testing.T) {
	conn := &PooledConnection{
		transport: &MockTransport{connected: true},
		target:    "test://target",
		inUse:     false,
		created:   time.Now(),
		lastUsed:  time.Now(),
		useCount:  1,
	}

	// Test healthy connection
	assert.True(t, conn.isHealthy())

	// Test unhealthy connection (disconnected)
	conn.transport.(*MockTransport).connected = false
	assert.False(t, conn.isHealthy())

	// Test old connection
	conn.created = time.Now().Add(-2 * time.Hour)
	assert.False(t, conn.isHealthy())
}

func TestPooledTransportWrapper(t *testing.T) {
	config := &Config{}
	factory := &MockTransportFactory{}
	pool := NewPool(config, "mock", factory)

	conn := &PooledConnection{
		transport: &MockTransport{},
		target:    "test://target",
		inUse:     true,
		created:   time.Now(),
		lastUsed:  time.Now(),
		useCount:  5,
	}

	wrapper := &PooledTransportWrapper{
		transport: conn.transport,
		pool:      pool,
		conn:      conn,
	}

	ctx := context.Background()

	// Test methods delegate to underlying transport
	assert.False(t, wrapper.IsConnected())

	// Test connect
	err := wrapper.Connect(ctx, "test://target")
	require.NoError(t, err)

	// Test execute
	result, err := wrapper.Execute(ctx, &Command{Cmd: []string{"echo", "test"}})
	require.NoError(t, err)
	assert.NotNil(t, result)

	// Test get info
	info := wrapper.GetInfo()
	assert.NotNil(t, info)
	assert.Equal(t, "true", info.Properties["pooled"])
	assert.Equal(t, "5", info.Properties["use_count"])
}

func TestPoolCleanupIdleConnections(t *testing.T) {
	config := &Config{
		Connection: ConnectionConfig{
			IdleTimeout: 10 * time.Millisecond, // Very short for testing
			MaxConns:    5,
		},
	}

	factory := &MockTransportFactory{}
	pool := NewPool(config, "mock", factory)

	ctx := context.Background()

	// Create connections and return them to pool
	transport1, _ := pool.Get(ctx, "test://target1")
	transport2, _ := pool.Get(ctx, "test://target2")

	transport1.Connect(ctx, "test://target1")
	transport2.Connect(ctx, "test://target2")

	transport1.Disconnect(ctx)
	transport2.Disconnect(ctx)

	// Wait for idle timeout
	time.Sleep(50 * time.Millisecond)

	// Run cleanup
	pool.CleanupIdleConnections()

	// Verify connections were cleaned up
	metrics := pool.GetMetrics()
	assert.Equal(t, 2, metrics.Created)
	assert.Greater(t, metrics.Destroyed, 0)
}

func TestPoolMaxIdle(t *testing.T) {
	config := &Config{
		Connection: ConnectionConfig{
			MaxConns: 5,
			MaxIdle:  1,
		},
	}

	factory := &MockTransportFactory{}
	pool := NewPool(config, "mock", factory)

	ctx := context.Background()

	// Create multiple connections and return them
	transport1, _ := pool.Get(ctx, "test://target")
	transport2, _ := pool.Get(ctx, "test://target")
	transport3, _ := pool.Get(ctx, "test://target")

	// Return first two
	transport1.Disconnect(ctx)
	transport2.Disconnect(ctx)

	// Try to return third (should exceed max idle and destroy one)
	transport3.Disconnect(ctx)

	// Verify metrics
	metrics := pool.GetMetrics()
	assert.Equal(t, 3, metrics.Created)
	assert.Greater(t, metrics.Destroyed, 0)
	assert.LessOrEqual(t, metrics.Idle, 1)
}

func TestPoolMaxLifetime(t *testing.T) {
	config := &Config{
		Connection: ConnectionConfig{
			MaxConns:    5,
			MaxIdle:     2,
			MaxLifetime: 10 * time.Millisecond, // Very short for testing
		},
	}

	factory := &MockTransportFactory{}
	pool := NewPool(config, "mock", factory)

	ctx := context.Background()

	// Create connection and return it
	transport, _ := pool.Get(ctx, "test://target")

	// Wait for max lifetime to pass
	time.Sleep(50 * time.Millisecond)

	// Return connection (should be destroyed due to age)
	err := transport.Disconnect(ctx)
	require.NoError(t, err)

	// Verify connection was destroyed
	metrics := pool.GetMetrics()
	assert.Equal(t, 1, metrics.Created)
	assert.Greater(t, metrics.Destroyed, 0)
}
