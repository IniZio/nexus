package sprites

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/superfly/sprites-go"
)

func TestSDKExitCodeFromErr(t *testing.T) {
	if c, ok := exitCodeFromErr(nil); c != 0 || ok {
		t.Fatalf("nil: got (%d,%v)", c, ok)
	}
	if c, ok := exitCodeFromErr(errors.New("boom")); c != 0 || ok {
		t.Fatalf("plain: got (%d,%v)", c, ok)
	}
	for _, want := range []int{0, 1, 137} {
		e := &sdk.ExitError{Code: want}
		if c, ok := exitCodeFromErr(e); c != int32(want) || !ok {
			t.Fatalf("direct %d: got (%d,%v)", want, c, ok)
		}
		w := fmt.Errorf("wrap: %w", fmt.Errorf("inner: %w", e))
		if c, ok := exitCodeFromErr(w); c != int32(want) || !ok {
			t.Fatalf("wrapped %d: got (%d,%v)", want, c, ok)
		}
	}
}

func TestSDKNewAPIEmptyToken(t *testing.T) {
	api, err := newSDKAPI("", "")
	if err == nil {
		t.Fatal("expected error for empty token")
	}
	if api != nil {
		t.Fatalf("expected nil API, got %v", api)
	}
	if strings.Contains(err.Error(), "secret-token-xyz") {
		t.Fatalf("error leaks token: %v", err)
	}
	if _, err := newSDKAPI("", "secret-token-xyz"); err == nil || strings.Contains(err.Error(), "secret-token-xyz") {
		t.Fatalf("empty token with secret org: err=%v", err)
	}
}

func TestSDKNewAPIConstructs(t *testing.T) {
	api, err := newSDKAPI("secret-token-xyz", "org")
	if err != nil {
		t.Fatal(err)
	}
	if api == nil {
		t.Fatal("nil API")
	}
}

func TestSDKSpriteExists(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if strings.HasSuffix(r.URL.Path, "/missing") {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"present","status":"running"}`))
	}))
	defer srv.Close()

	api := &sdkAPI{c: sdk.New("tok", sdk.WithBaseURL(srv.URL))}
	ctx := context.Background()

	ok, err := api.SpriteExists(ctx, "missing")
	if err != nil || ok {
		t.Fatalf("404: got (%v,%v)", ok, err)
	}
	ok, err = api.SpriteExists(ctx, "present")
	if err != nil || !ok {
		t.Fatalf("200: got (%v,%v)", ok, err)
	}
	if !strings.Contains(auth, "tok") {
		t.Fatalf("auth header %q lacks token", auth)
	}
}
