package cred

import (
	"context"
	"net/http"
	"testing"
)

func TestMITM401ForceRefreshReplaysOnce(t *testing.T) {
	t.Parallel()
	refreshCalls := 0
	forceRefresh := func(_ context.Context) (string, error) {
		refreshCalls++
		return "new-vault-token", nil
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer old-token")

	requestsSeen := 0
	doRequest := func(r *http.Request) (*http.Response, error) {
		requestsSeen++
		return &http.Response{StatusCode: http.StatusOK, Request: r}, nil
	}

	original401 := &http.Response{StatusCode: http.StatusUnauthorized, Request: req}

	resp, err := Handle401Once(context.Background(), req, original401, forceRefresh, doRequest)
	if err != nil {
		t.Fatalf("Handle401Once: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("want 200, got %d", resp.StatusCode)
	}
	if refreshCalls != 1 {
		t.Errorf("want exactly 1 forceRefresh call, got %d", refreshCalls)
	}
	if requestsSeen != 1 {
		t.Errorf("want exactly 1 replay, got %d", requestsSeen)
	}
	if got := resp.Request.Header.Get("Authorization"); got != "Bearer new-vault-token" {
		t.Errorf("replayed Authorization = %q, want Bearer new-vault-token", got)
	}
}

func TestMITM401ForceRefreshReplaysOnce_SecondUnauthorizedNotRetried(t *testing.T) {
	t.Parallel()
	refreshCalls := 0
	forceRefresh := func(_ context.Context) (string, error) {
		refreshCalls++
		return "refreshed-token", nil
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.github.com/user", nil)

	doRequest := func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Request: r}, nil
	}

	original401 := &http.Response{StatusCode: http.StatusUnauthorized, Request: req}

	resp, err := Handle401Once(context.Background(), req, original401, forceRefresh, doRequest)
	if err != nil {
		t.Fatalf("Handle401Once: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401 on second attempt, got %d", resp.StatusCode)
	}
	if refreshCalls != 1 {
		t.Errorf("want exactly 1 forceRefresh call (not retried on second 401), got %d", refreshCalls)
	}
}

func TestMITM401ForceRefreshReplaysOnce_NonUnauthorizedPassThrough(t *testing.T) {
	t.Parallel()
	refreshCalled := false
	forceRefresh := func(_ context.Context) (string, error) {
		refreshCalled = true
		return "should-not-be-used", nil
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.github.com/user", nil)
	ok200 := &http.Response{StatusCode: http.StatusOK, Request: req}

	resp, err := Handle401Once(context.Background(), req, ok200, forceRefresh, nil)
	if err != nil {
		t.Fatalf("Handle401Once: %v", err)
	}
	if resp != ok200 {
		t.Error("non-401 response must be returned as-is")
	}
	if refreshCalled {
		t.Error("forceRefresh must not be called for non-401 responses")
	}
}
