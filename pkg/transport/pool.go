package transport

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type Pool struct {
	config        *Config
	transportType string
	factory       TransportFactory
	conns         map[string][]*PooledConnection
	mu            sync.RWMutex
	metrics       *PoolMetrics
}

type PooledConnection struct {
	transport Transport
	target    string
	inUse     bool
	created   time.Time
	lastUsed  time.Time
	useCount  int
	mu        sync.Mutex
}

type TransportFactory interface {
	CreateTransport(config *Config) (Transport, error)
}

type PoolMetrics struct {
	Created     int           `json:"created"`
	Destroyed   int           `json:"destroyed"`
	Active      int           `json:"active"`
	Idle        int           `json:"idle"`
	TotalReused int           `json:"total_reused"`
	WaitTime    time.Duration `json:"wait_time"`
}

func NewPool(config *Config, transportType string, factory TransportFactory) *Pool {
	return &Pool{
		config:        config,
		transportType: transportType,
		factory:       factory,
		conns:         make(map[string][]*PooledConnection),
		metrics: &PoolMetrics{
			Created:   0,
			Destroyed: 0,
			Active:    0,
			Idle:      0,
		},
	}
}

func (p *Pool) Get(ctx context.Context, target string) (Transport, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conns[target] == nil {
		p.conns[target] = make([]*PooledConnection, 0)
	}

	maxConns := p.config.Connection.MaxConns
	if maxConns == 0 {
		maxConns = 10
	}

	for _, conn := range p.conns[target] {
		conn.mu.Lock()
		if !conn.inUse && conn.isHealthy() {
			conn.inUse = true
			conn.lastUsed = time.Now()
			conn.useCount++
			p.metrics.TotalReused++
			p.metrics.Active++
			p.metrics.Idle--
			conn.mu.Unlock()
			return &PooledTransportWrapper{
				transport: conn.transport,
				pool:      p,
				conn:      conn,
			}, nil
		}
		conn.mu.Unlock()
	}

	if len(p.conns[target]) < maxConns {
		transport, err := p.createTransport(target)
		if err != nil {
			return nil, fmt.Errorf("failed to create transport: %w", err)
		}

		conn := &PooledConnection{
			transport: transport,
			target:    target,
			inUse:     true,
			created:   time.Now(),
			lastUsed:  time.Now(),
			useCount:  1,
		}

		p.conns[target] = append(p.conns[target], conn)
		p.metrics.Created++
		p.metrics.Active++

		return &PooledTransportWrapper{
			transport: conn.transport,
			pool:      p,
			conn:      conn,
		}, nil
	}

	return nil, fmt.Errorf("connection pool exhausted for target %s", target)
}

func (p *Pool) put(conn *PooledConnection) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	conn.mu.Lock()
	defer conn.mu.Unlock()

	if !conn.inUse {
		return nil
	}

	conn.inUse = false
	conn.lastUsed = time.Now()
	p.metrics.Active--
	p.metrics.Idle++

	maxIdle := p.config.Connection.MaxIdle
	if maxIdle == 0 {
		maxIdle = 5
	}

	idleConns := 0
	for _, c := range p.conns[conn.target] {
		if c == conn {
			idleConns++ // Count self as idle (we just set inUse=false at line 129)
			continue
		}
		c.mu.Lock()
		if !c.inUse {
			idleConns++
		}
		c.mu.Unlock()
	}

	if idleConns > maxIdle {
		return p.destroyConnectionLocked(conn)
	}

	maxLifetime := p.config.Connection.MaxLifetime
	if maxLifetime == 0 {
		maxLifetime = 1 * time.Hour
	}

	if time.Since(conn.created) > maxLifetime {
		return p.destroyConnectionLocked(conn)
	}

	return nil
}

func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var lastErr error
	for target, conns := range p.conns {
		for _, conn := range conns {
			conn.mu.Lock()
			if err := p.destroyConnectionLocked(conn); err != nil {
				lastErr = err
			}
			conn.mu.Unlock()
		}
		delete(p.conns, target)
	}

	return lastErr
}

func (p *Pool) GetMetrics() *PoolMetrics {
	p.mu.RLock()
	defer p.mu.RUnlock()

	metrics := *p.metrics
	metrics.Active = 0
	metrics.Idle = 0

	for _, conns := range p.conns {
		for _, conn := range conns {
			conn.mu.Lock()
			if conn.inUse {
				metrics.Active++
			} else {
				metrics.Idle++
			}
			conn.mu.Unlock()
		}
	}

	return &metrics
}

func (p *Pool) CleanupIdleConnections() {
	p.mu.Lock()
	defer p.mu.Unlock()

	idleTimeout := p.config.Connection.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = 30 * time.Minute
	}

	now := time.Now()
	for target, conns := range p.conns {
		var remainingConns []*PooledConnection
		for _, conn := range conns {
			conn.mu.Lock()
			if !conn.inUse && now.Sub(conn.lastUsed) > idleTimeout {
				p.destroyConnectionLocked(conn)
			} else {
				remainingConns = append(remainingConns, conn)
			}
			conn.mu.Unlock()
		}
		p.conns[target] = remainingConns
	}
}

func (p *Pool) createTransport(target string) (Transport, error) {
	config := *p.config
	config.Target = target
	return p.factory.CreateTransport(&config)
}

func (p *Pool) destroyConnectionLocked(conn *PooledConnection) error {
	if conn.inUse {
		return fmt.Errorf("cannot destroy connection in use")
	}

	if err := conn.transport.Disconnect(context.Background()); err != nil {
		return fmt.Errorf("failed to disconnect transport: %w", err)
	}

	conns := p.conns[conn.target]
	for i, c := range conns {
		if c == conn {
			p.conns[conn.target] = append(conns[:i], conns[i+1:]...)
			break
		}
	}

	p.metrics.Destroyed++
	p.metrics.Idle--

	return nil
}

func (p *Pool) destroyConnection(conn *PooledConnection) error {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	return p.destroyConnectionLocked(conn)
}

func (c *PooledConnection) isHealthy() bool {
	if c.transport == nil {
		return false
	}

	if !c.transport.IsConnected() {
		return false
	}

	age := time.Since(c.created)
	maxAge := 1 * time.Hour
	if age > maxAge {
		return false
	}

	return true
}

type PooledTransportWrapper struct {
	transport Transport
	pool      *Pool
	conn      *PooledConnection
}

func (w *PooledTransportWrapper) Connect(ctx context.Context, target string) error {
	return w.transport.Connect(ctx, target)
}

func (w *PooledTransportWrapper) Disconnect(ctx context.Context) error {
	return w.pool.put(w.conn)
}

func (w *PooledTransportWrapper) IsConnected() bool {
	return w.transport.IsConnected()
}

func (w *PooledTransportWrapper) Execute(ctx context.Context, cmd *Command) (*Result, error) {
	return w.transport.Execute(ctx, cmd)
}

func (w *PooledTransportWrapper) Upload(ctx context.Context, localPath, remotePath string) error {
	return w.transport.Upload(ctx, localPath, remotePath)
}

func (w *PooledTransportWrapper) Download(ctx context.Context, remotePath, localPath string) error {
	return w.transport.Download(ctx, remotePath, localPath)
}

func (w *PooledTransportWrapper) GetInfo() *Info {
	info := w.transport.GetInfo()
	info.Properties["pooled"] = "true"
	info.Properties["use_count"] = fmt.Sprintf("%d", w.conn.useCount)
	info.Properties["created"] = w.conn.created.Format(time.RFC3339)
	return info
}
