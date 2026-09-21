package vm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
)

const chAPIBase = "http://ch/api/v1"

type chVMConfig struct {
	Payload chVMPayload  `json:"payload"`
	CPUs    *chVMCPUs    `json:"cpus,omitempty"`
	Memory  *chVMMemory  `json:"memory,omitempty"`
	Serial  *chVMSerial  `json:"serial,omitempty"`
	Disks   []chVMDisk   `json:"disks,omitempty"`
	Balloon *chVMBalloon `json:"balloon,omitempty"`
	Vsock   *chVMVsock   `json:"vsock,omitempty"`
}

type chVMPayload struct {
	Kernel  string `json:"kernel,omitempty"`
	Cmdline string `json:"cmdline,omitempty"`
}

type chVMCPUs struct {
	BootVCPUs uint32 `json:"boot_vcpus"`
	MaxVCPUs  uint32 `json:"max_vcpus"`
	Nested    bool   `json:"nested"`
}

type chVMMemory struct {
	SizeBytes uint64 `json:"size"`
}

type chVMSerial struct {
	Mode string `json:"mode"`
	File string `json:"file,omitempty"`
}

type chVMDisk struct {
	Path      string `json:"path"`
	ImageType string `json:"image_type,omitempty"`
	ReadOnly  bool   `json:"readonly,omitempty"`
}

type chVMBalloon struct {
	SizeBytes    uint64 `json:"size"`
	DeflateOnOOM bool   `json:"deflate_on_oom,omitempty"`
}

type chVMVsock struct {
	CID    uint64 `json:"cid"`
	Socket string `json:"socket"`
}

type chErrorResp []string

type chClient struct {
	socketPath string
	http       *http.Client
}

func newCHClient(socketPath string) *chClient {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dialer := net.Dialer{}
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	return &chClient{
		socketPath: socketPath,
		http:       client,
	}
}

func (c *chClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", chAPIBase+"/vmm.ping", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer chDrainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping failed: status %d", resp.StatusCode)
	}
	return nil
}

func (c *chClient) VMCreate(ctx context.Context, cfg chVMConfig) error {
	body, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", chAPIBase+"/vm.create", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("vm.create failed: status %d: %s", resp.StatusCode, body)
	}
	chDrainClose(resp)
	return nil
}

func (c *chClient) VMBoot(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", chAPIBase+"/vm.boot", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer chDrainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("vm.boot failed: status %d", resp.StatusCode)
	}
	return nil
}

func (c *chClient) VMShutdown(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", chAPIBase+"/vm.shutdown", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer chDrainClose(resp)
	// Accept 204, 404, 405, 500 (not running or already stopped)
	if resp.StatusCode == http.StatusNoContent ||
		resp.StatusCode == http.StatusNotFound ||
		resp.StatusCode == http.StatusMethodNotAllowed ||
		resp.StatusCode == http.StatusInternalServerError {
		return nil
	}
	return fmt.Errorf("vm.shutdown failed: status %d", resp.StatusCode)
}

func (c *chClient) VMMShutdown(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "PUT", chAPIBase+"/vmm.shutdown", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer chDrainClose(resp)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("vmm.shutdown failed: status %d", resp.StatusCode)
	}
	return nil
}

func chIsAbsent(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

func chDrainClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()              //nolint:errcheck
}
