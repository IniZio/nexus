package linkflow_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/IniZio/nexus/internal/core/vault/linkflow"
)

func TestLoopbackCallbackStateMismatchRejected(t *testing.T) {
	ctx := t.Context()
	l, err := linkflow.Listen(ctx, ":0", "correct-state")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	go func() {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/?code=abc&state=wrong-state", l.Port()))
		if err != nil {
			return
		}
		resp.Body.Close()
	}()

	_, err = l.Wait(ctx)
	if err != linkflow.ErrStateMismatch {
		t.Fatalf("want ErrStateMismatch, got %v", err)
	}
}

func TestLoopbackCallbackSuccess(t *testing.T) {
	ctx := t.Context()
	l, err := linkflow.Listen(ctx, ":0", "my-state")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer l.Close()

	go func() {
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/?code=mycode&state=my-state", l.Port()))
		if err != nil {
			return
		}
		resp.Body.Close()
	}()

	code, err := l.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != "mycode" {
		t.Errorf("code mismatch: got %q want %q", code, "mycode")
	}
}
