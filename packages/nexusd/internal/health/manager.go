package health

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

type HealthChecker struct {
	workspaceID  string
	checks       []HealthCheck
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	status       HealthStatus
	monitoring   bool
}

type HealthCheck struct {
	Name     string
	Type     string
	Target   string
	Interval time.Duration
	Timeout  time.Duration
}

type HealthStatus struct {
	Healthy   bool
	Checks    []CheckResult
	LastCheck time.Time
}

type CheckResult struct {
	Name    string
	Healthy bool
	Error   string
	Latency time.Duration
}

func NewHealthChecker(workspaceID string) *HealthChecker {
	ctx, cancel := context.WithCancel(context.Background())
	return &HealthChecker{
		workspaceID: workspaceID,
		checks:      []HealthCheck{},
		ctx:         ctx,
		cancel:      cancel,
		status: HealthStatus{
			Healthy: false,
			Checks:  []CheckResult{},
		},
	}
}

func (h *HealthChecker) AddCheck(check HealthCheck) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if check.Interval == 0 {
		check.Interval = 30 * time.Second
	}
	if check.Timeout == 0 {
		check.Timeout = 10 * time.Second
	}

	h.checks = append(h.checks, check)
}

func (h *HealthChecker) Check() HealthStatus {
	h.mu.Lock()
	defer h.mu.Unlock()

	results := make([]CheckResult, 0, len(h.checks))
	allHealthy := true

	for _, check := range h.checks {
		result := h.runCheck(check)
		results = append(results, result)
		if !result.Healthy {
			allHealthy = false
		}
	}

	h.status = HealthStatus{
		Healthy:   allHealthy,
		Checks:    results,
		LastCheck: time.Now(),
	}

	return h.status
}

func (h *HealthChecker) runCheck(check HealthCheck) CheckResult {
	start := time.Now()

	var err error
	var healthy bool

	switch check.Type {
	case "http", "https":
		healthy, err = h.checkHTTP(check)
	case "tcp":
		healthy, err = h.checkTCP(check)
	case "script":
		healthy, err = h.checkScript(check)
	default:
		err = fmt.Errorf("unknown check type: %s", check.Type)
		healthy = false
	}

	latency := time.Since(start)

	errorMsg := ""
	if err != nil {
		errorMsg = err.Error()
	}

	return CheckResult{
		Name:    check.Name,
		Healthy: healthy,
		Error:   errorMsg,
		Latency: latency,
	}
}

func (h *HealthChecker) checkHTTP(check HealthCheck) (bool, error) {
	ctx, cancel := context.WithTimeout(h.ctx, check.Timeout)
	defer cancel()

	url := check.Target
	if check.Type == "http" && len(url) > 7 && url[:7] != "http://" {
		url = "http://" + url
	}
	if check.Type == "https" && len(url) > 8 && url[:8] != "https://" {
		url = "https://" + url
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err
	}

	client := &http.Client{
		Timeout: check.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	return resp.StatusCode >= 200 && resp.StatusCode < 400, nil
}

func (h *HealthChecker) checkTCP(check HealthCheck) (bool, error) {
	conn, err := net.DialTimeout("tcp", check.Target, check.Timeout)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	return true, nil
}

func (h *HealthChecker) checkScript(check HealthCheck) (bool, error) {
	return false, fmt.Errorf("script health checks not yet implemented")
}

func (h *HealthChecker) StartMonitoring() {
	h.mu.Lock()
	if h.monitoring {
		h.mu.Unlock()
		return
	}
	h.monitoring = true
	h.mu.Unlock()

	go h.monitorLoop()
}

func (h *HealthChecker) monitorLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	h.Check()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			h.Check()
		}
	}
}

func (h *HealthChecker) StopMonitoring() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.monitoring {
		h.cancel()
		h.monitoring = false
		h.ctx, h.cancel = context.WithCancel(context.Background())
	}
}

func (h *HealthChecker) GetStatus() HealthStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.status
}

func (h *HealthChecker) WorkspaceID() string {
	return h.workspaceID
}

type HealthCheckParser struct{}

func NewHealthCheckParser() *HealthCheckParser {
	return &HealthCheckParser{}
}

func (p *HealthCheckParser) ParseComposeHealthcheck(serviceName string, healthcheck map[string]interface{}) []HealthCheck {
	var checks []HealthCheck

	test, ok := healthcheck["test"].(string)
	if !ok {
		return checks
	}

	interval, _ := healthcheck["interval"].(string)
	timeout, _ := healthcheck["timeout"].(string)

	intervalDur := parseDuration(interval, 30*time.Second)
	timeoutDur := parseDuration(timeout, 10*time.Second)

	check := HealthCheck{
		Name:     serviceName,
		Type:     "http",
		Target:   "",
		Interval: intervalDur,
		Timeout:  timeoutDur,
	}

	if test == "CMD" || test == "CMD-SHELL" {
		log.Printf("[health] Service %s has shell-based healthcheck, skipping auto-conversion", serviceName)
		return checks
	}

	if test == "NONE" {
		return checks
	}

	checks = append(checks, check)
	return checks
}

func parseDuration(s string, defaultDur time.Duration) time.Duration {
	if s == "" {
		return defaultDur
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return defaultDur
	}
	return dur
}
