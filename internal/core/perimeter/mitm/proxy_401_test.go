package mitm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/perimeter/mitm"
)

func TestMITMCallsForceRefreshOn401(t *testing.T) {
	t.Parallel()

	var reqCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reqCount.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	refreshed := false
	rfn := cred.ForceRefreshFn(func(_ context.Context) (string, error) {
		refreshed = true
		return "new-token", nil
	})

	broker := cred.NewBroker()
	sid := newSandboxID(50)
	proxyServer := newTestProxy(t, mitm.Config{
		SandboxID: sid,
		Broker:    broker,
		AllowAll:  true,
		ForceRefreshFns: map[string]cred.ForceRefreshFn{
			"example.test": rfn,
		},
	}, upstream.Listener.Addr().String())
	t.Cleanup(proxyServer.Close)

	client := proxyClient(proxyServer.URL)
	resp, err := client.Get("http://example.test/resource")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if !refreshed {
		t.Error("ForceRefreshFn was not called on 401; MITM 401-retry wiring is broken")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("final response status = %d, want 200", resp.StatusCode)
	}
}
