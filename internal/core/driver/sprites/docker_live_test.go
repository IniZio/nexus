//go:build spriteslive

package sprites_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
)

func TestDockerPresetLive_Sprites(t *testing.T) {
	if os.Getenv("SPRITES_TOKEN") == "" && os.Getenv("SPRITES_API_TOKEN") == "" {
		t.Skip("set SPRITES_TOKEN")
	}
	drv := liveBrokerDriver(t, t.TempDir())
	id := domain.NewSandboxID()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	start := time.Now()
	defer func() {
		if err := drv.Deprovision(context.Background(), id); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()
	if err := drv.Provision(ctx, id, sprites.Spec{Presets: []string{sprites.PresetDocker}}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("Provision+docker install took %s", time.Since(start))

	run := func(argv ...string) (int32, string) {
		var out bytes.Buffer
		code, err := drv.Exec(ctx, id, driver.ExecOptions{Argv: argv, Stdout: &out, Stderr: &out})
		if err != nil {
			t.Fatalf("exec %v: %v", argv, err)
		}
		return code, out.String()
	}
	t0 := time.Now()
	code, o := run("docker", "run", "--rm", "hello-world")
	t.Logf("docker run hello-world took %s code=%d", time.Since(t0), code)
	if code != 0 || !strings.Contains(o, "Hello from Docker!") {
		t.Fatalf("hello-world: code=%d %q", code, o)
	}
	code, o = run("curl", "-sS", "-m", "10", "-o", "/dev/null", "https://archive.ubuntu.com")
	t.Logf("archive.ubuntu.com after install: code=%d %q", code, o)
	if code == 0 {
		t.Fatal("apt host must be denied after install")
	}
}
