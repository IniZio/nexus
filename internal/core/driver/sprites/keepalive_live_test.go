//go:build spriteslive

package sprites_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/superfly/sprites-go"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
)

// TestKeepaliveLive measures process continuity and sprite status with and without an open exec session.
func TestKeepaliveLive(t *testing.T) {
	tok := os.Getenv("SPRITES_TOKEN")
	if tok == "" {
		t.Skip("set SPRITES_TOKEN")
	}
	drv, err := sprites.New(sprites.Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id := domain.NewSandboxID()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	if err := drv.Provision(ctx, id, sprites.Spec{}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	defer func() {
		if err := drv.Deprovision(context.Background(), id); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()
	client := sdk.New(tok, sdk.WithDisableControl())
	name := sprites.SpriteName(id)
	status := func() string {
		s, err := client.GetSprite(ctx, name)
		if err != nil {
			return "err:" + err.Error()
		}
		return s.Status
	}
	observe := func(label string, d, step time.Duration) map[string]int {
		seen := map[string]int{}
		for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(step) {
			st := status()
			seen[st]++
			t.Logf("%s t-%s status=%s", label, time.Until(end).Round(time.Second), st)
		}
		return seen
	}
	run := func(c context.Context, argv ...string) string {
		var out bytes.Buffer
		_, _ = drv.Exec(c, id, driver.ExecOptions{Argv: argv, Cwd: "/tmp", Stdout: &out, Stderr: &out})
		return out.String()
	}
	loop := func(f string) string {
		return "while :; do date +%s >> " + f + "; sleep 5; done"
	}
	gaps := func(f string) string {
		var prev, maxGap, n int
		for _, l := range strings.Fields(run(ctx, "cat", f)) {
			v, _ := strconv.Atoi(l)
			if prev != 0 && v-prev > maxGap {
				maxGap = v - prev
			}
			prev = v
			n++
		}
		return fmt.Sprintf("beats=%d maxGap=%ds", n, maxGap)
	}

	// A: detached loop, no session open.
	run(ctx, "sh", "-c", "nohup sh -c '"+loop("/tmp/hbA")+"' >/dev/null 2>&1 </dev/null &")
	t.Logf("A detached/no session: %v", observe("A", 330*time.Second, 30*time.Second))
	t.Logf("A heartbeat: %s", gaps("/tmp/hbA"))

	// B: loop as the foreground of a long-lived exec session.
	sessCtx, stop := context.WithCancel(ctx)
	sessDone := make(chan time.Time, 1)
	go func() { run(sessCtx, "sh", "-c", loop("/tmp/hbB")); sessDone <- time.Now() }()
	time.Sleep(5 * time.Second)
	t.Logf("B session open: %v", observe("B", 330*time.Second, 30*time.Second))
	select {
	case at := <-sessDone:
		t.Logf("B session ended early at %s", at.Format(time.RFC3339))
	default:
		t.Log("B session still open at end")
	}
	t.Logf("B heartbeat: %s", gaps("/tmp/hbB"))
	stop()
}
