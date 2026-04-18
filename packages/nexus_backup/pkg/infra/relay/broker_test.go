package authrelay

import (
	"testing"
	"time"
)

func TestBroker_ConsumeOnlyOnce(t *testing.T) {
	b := NewBroker()
	token := b.Mint("ws-1", map[string]string{"A": "1"}, time.Minute)

	env, ok := b.Consume(token, "ws-1")
	if !ok {
		t.Fatal("expected first consume to succeed")
	}
	if env["A"] != "1" {
		t.Fatalf("expected env value 1, got %q", env["A"])
	}

	_, ok = b.Consume(token, "ws-1")
	if ok {
		t.Fatal("expected second consume to fail")
	}
}

func TestBroker_ExpiresGrant(t *testing.T) {
	b := NewBroker()
	now := time.Now()
	b.now = func() time.Time { return now }
	token := b.Mint("ws-1", map[string]string{"A": "1"}, time.Second)
	b.now = func() time.Time { return now.Add(2 * time.Second) }

	_, ok := b.Consume(token, "ws-1")
	if ok {
		t.Fatal("expected consume to fail after expiry")
	}
}
