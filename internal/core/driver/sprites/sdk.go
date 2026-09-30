package sprites

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	sdk "github.com/superfly/sprites-go"
)

// sdkAPI implements API over sprites-go. The token alone authenticates;
// org is stored for callers but the SDK does not need it.
type sdkAPI struct {
	c   *sdk.Client
	org string
}

func newSDKAPI(token, org string) (API, error) {
	if token == "" {
		return nil, errors.New("sprites: API token is empty")
	}
	return &sdkAPI{c: sdk.New(token, sdk.WithDisableControl()), org: org}, nil
}

func (a *sdkAPI) CreateSprite(ctx context.Context, name string) error {
	return withRetry(ctx, func() error {
		_, err := a.c.CreateSprite(ctx, name, nil)
		return err
	})
}

func (a *sdkAPI) SpriteExists(ctx context.Context, name string) (bool, error) {
	_, err := a.c.GetSprite(ctx, name)
	if err == nil {
		return true, nil
	}
	if ae := sdk.IsAPIError(err); ae != nil && ae.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if strings.HasPrefix(err.Error(), "sprite not found") {
		return false, nil
	}
	return false, err
}

func (a *sdkAPI) DeleteSprite(ctx context.Context, name string) error {
	return a.c.DeleteSprite(ctx, name)
}

func (a *sdkAPI) SetNetworkPolicy(ctx context.Context, name string, p *sdk.NetworkPolicy) error {
	return withRetry(ctx, func() error {
		return a.c.UpdateNetworkPolicy(ctx, name, p)
	})
}

func (a *sdkAPI) Exec(ctx context.Context, name string, req ExecRequest) (int32, error) {
	if len(req.Argv) == 0 {
		return 0, errors.New("sprites: empty argv")
	}
	cmd := a.c.Sprite(name).CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	cmd.Dir = req.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = req.Stdin, req.Stdout, req.Stderr

	keys := make([]string, 0, len(req.Env))
	for k := range req.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys)+1)
	for _, k := range keys {
		env = append(env, k+"="+req.Env[k])
	}
	if req.TTY && req.Term != "" {
		env = append(env, "TERM="+req.Term)
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	if req.TTY {
		cmd.SetTTY(true)
		if err := cmd.SetTTYSize(req.Rows, req.Cols); err != nil {
			return 0, fmt.Errorf("sprites: set tty size: %w", err)
		}
	}

	if err := withRetry(ctx, cmd.Start); err != nil {
		return 0, err
	}

	if req.Resize != nil {
		done := make(chan struct{})
		defer close(done)
		go func() {
			for {
				select {
				case <-done:
					return
				case <-ctx.Done():
					return
				case w, ok := <-req.Resize:
					if !ok {
						return
					}
					_ = cmd.Resize(w.Rows, w.Cols)
				}
			}
		}()
	}

	if err := cmd.Wait(); err != nil {
		if code, ok := exitCodeFromErr(err); ok {
			return code, nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return 0, cerr
		}
		return 0, err
	}
	return 0, nil
}

// exitCodeFromErr reports the remote exit code when err is or wraps an
// *sdk.ExitError.
func exitCodeFromErr(err error) (code int32, ok bool) {
	var ee *sdk.ExitError
	if errors.As(err, &ee) {
		return int32(ee.Code), true
	}
	return 0, false
}
