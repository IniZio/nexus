package sprites

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
)

const tightenTimeout = 30 * time.Second

// hasGoMod reports whether the seeded commit carries a root go.mod.
func hasGoMod(ctx context.Context, hostRepoDir, sha string) bool {
	_, err := hostGit(ctx, hostRepoDir, "cat-file", "-e", sha+":go.mod")
	return err == nil
}

// warmGo opens egress to GoWarmHost, fetches the go toolchain and modules, then
// restores the default policy on every path. A failed restore is an error
// (fail closed); a failed warm is not: go retries on demand under the default policy.
func (d *Driver) warmGo(ctx context.Context, id domain.SandboxID, guestDir string) (err error) {
	spec, _ := d.Spec(id)
	if spec.OpenEgress {
		return nil
	}
	name := SpriteName(id)
	hosts := append(slices.Clone(spec.AllowedHosts), GoToolchainHosts...)
	tight := BuildPolicy(hosts, spec.IncludeDefaults, false)
	if err := d.api.SetNetworkPolicy(ctx, name, BuildPolicy(append(slices.Clone(hosts), GoWarmHost), spec.IncludeDefaults, false)); err != nil {
		return fmt.Errorf("sprites warm: open egress window: %w", err)
	}
	defer func() {
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tightenTimeout)
		defer cancel()
		if terr := d.api.SetNetworkPolicy(tctx, name, tight); terr != nil {
			err = errors.Join(err, fmt.Errorf("sprites warm: tighten egress (GCS may still be open): %w", terr))
		}
	}()
	env := map[string]string{"GOTOOLCHAIN": "auto"}
	for _, argv := range [][]string{{"go", "version"}, {"go", "mod", "download"}} {
		if werr := d.guestRun(ctx, id, ExecRequest{Argv: argv, Env: env, Dir: guestDir}); werr != nil {
			break
		}
	}
	return nil
}
