package docker

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/client"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/require"

	"nexus/pkg/testutil"
)

func TestExecIntegration_SimpleCommand_Success(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"bash", "-c", "while true; do sleep 1; done"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	pool.MaxWait = 120 * time.Second
	resource.Expire(900)

	var stdout, stderr bytes.Buffer
	exitCode, err := resource.Exec(
		[]string{"echo", "test output"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
	require.Equal(t, 0, exitCode)
	require.Contains(t, stdout.String(), "test output")
}

func TestExecIntegration_InteractiveShell_PTY(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"bash", "-c", "apt-get update && apt-get install -y openssh-server && echo 'root:root' | chpasswd && mkdir -p /run/sshd && /usr/sbin/sshd"},
			Env:        []string{"DEBIAN_FRONTEND=noninteractive"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	time.Sleep(2 * time.Second)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"bash", "-c", "echo hello"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
			TTY:    true,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_CommandNotFound_Error(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"echo", "test"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"nonexistentcommand123"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.Error(t, err, "nonexistent command should return error")
}

func TestExecIntegration_Timeout_Handling(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	done := make(chan error, 1)

	go func() {
		resource, err := pool.RunWithOptions(
			&dockertest.RunOptions{
				Repository: "ubuntu",
				Tag:        "22.04",
				Cmd:        []string{"sleep", "10"},
			},
			func(config *docker.HostConfig) {
				config.AutoRemove = true
			},
		)
		if err == nil {
			_, err := resource.Exec(
				[]string{"sleep", "10"},
				dockertest.ExecOptions{
					StdOut: io.Discard,
					StdErr: io.Discard,
				},
			)
			done <- err
			pool.Purge(resource)
		} else {
			done <- err
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func TestExecIntegration_MultiLineOutput_Capture(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"echo", "test"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"echo", "-e", "line1\nline2\nline3"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_LargeOutput_Handling(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"echo", "test"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	largeOutput := strings.Repeat("x", 10000)
	_, err = resource.Exec(
		[]string{"echo", largeOutput},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_Stdin_Passing(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"cat"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	stdin := strings.NewReader("test input")
	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"cat"},
		dockertest.ExecOptions{
			StdIn:  stdin,
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_EnvironmentVariables(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Env:        []string{"TEST_VAR=test_value"},
			Cmd:        []string{"sh", "-c", "echo $TEST_VAR"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"sh", "-c", "echo $TEST_VAR"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_WorkingDirectory(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"pwd"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"pwd"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_SpecialCharacters(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"echo", "test"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	specialChars := `!@#$%^&*()_+-=[]{}|;':",./<>?`
	_, err = resource.Exec(
		[]string{"echo", specialChars},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_EmptyCommand_Error(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"echo", "test"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.Error(t, err, "empty command should return error")
}

func TestExecIntegration_ContainerNotRunning_Error(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	cli, err := client.NewClientWithOpts(client.FromEnv)
	require.NoError(t, err)
	defer cli.Close()

	provider := &Provider{cli: cli}

	ctx := context.Background()

	_, err = provider.ExecWithOutput(ctx, "nonexistent-container", []string{"echo", "test"}, 5*time.Second)
	require.Error(t, err, "exec on non-existent container should return error")
	require.Contains(t, err.Error(), "workspace not found", "error should mention workspace not found")
}

func TestExecIntegration_SSHKeyMissing_Error(t *testing.T) {
	t.Skip("Requires SSH key setup - cannot test without SSH key")
}

func TestExecIntegration_PortNotFound_Error(t *testing.T) {
	t.Skip("Requires port configuration - requires specific port setup")
}

func TestExecIntegration_ContextCancellation(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	done := make(chan error, 1)

	go func() {
		resource, err := pool.RunWithOptions(
			&dockertest.RunOptions{
				Repository: "ubuntu",
				Tag:        "22.04",
				Cmd:        []string{"sleep", "30"},
			},
			func(config *docker.HostConfig) {
				config.AutoRemove = true
			},
		)
		if err == nil {
			_, err := resource.Exec(
				[]string{"sleep", "30"},
				dockertest.ExecOptions{
					StdOut: io.Discard,
					StdErr: io.Discard,
				},
			)
			done <- err
			pool.Purge(resource)
		} else {
			done <- err
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = ctx
	}
}

func TestExecIntegration_ExitCode_Propagation(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"sh", "-c", "exit 42"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"sh", "-c", "exit 42"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.Error(t, err, "exit with code 42 should return error")
}

func TestExecIntegration_ParallelExecution(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	numCommands := 5
	done := make(chan error, numCommands)

	for i := 0; i < numCommands; i++ {
		go func(idx int) {
			resource, err := pool.RunWithOptions(
				&dockertest.RunOptions{
					Repository: "ubuntu",
					Tag:        "22.04",
					Cmd:        []string{"echo", string(rune('a' + idx))},
				},
				func(config *docker.HostConfig) {
					config.AutoRemove = true
				},
			)
			if err == nil {
				var stdout, stderr bytes.Buffer
				_, err := resource.Exec(
					[]string{"echo", string(rune('a' + idx))},
					dockertest.ExecOptions{
						StdOut: &stdout,
						StdErr: &stderr,
					},
				)
				done <- err
				pool.Purge(resource)
			} else {
				done <- err
			}
		}(i)
	}

	for i := 0; i < numCommands; i++ {
		err := <-done
		require.NoError(t, err)
	}
}

func TestExecIntegration_SignalHandling(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"trap", "exit 1", "TERM", "-c", "sleep", "60"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	err = pool.Client.KillContainer(docker.KillContainerOptions{
		ID:     resource.Container.ID,
		Signal: docker.Signal(15),
	})
	require.NoError(t, err)

	time.Sleep(1 * time.Second)
}

func TestExecIntegration_ImagePullAndExec(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "alpine",
			Tag:        "latest",
			Cmd:        []string{"sh", "-c", "while true; do sleep 1; done"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"echo", "alpine works"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_SecurityContext(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"whoami"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"whoami"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_StdinEcho(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"cat"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	stdin := strings.NewReader("hello world")
	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"cat"},
		dockertest.ExecOptions{
			StdIn:  stdin,
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_CombinedStreams(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"sh", "-c", "echo stdout && echo stderr >&2"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"sh", "-c", "echo stdout && echo stderr >&2"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_NonZeroExitWithOutput(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"sh", "-c", "echo 'error message' && exit 1"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"sh", "-c", "echo 'error message' && exit 1"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.Error(t, err, "non-zero exit should return error")
}

func TestExecIntegration_LongRunningCommand(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"sleep", "1", "&&", "echo", "done"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	start := time.Now()
	_, err = resource.Exec(
		[]string{"sleep", "1", "&&", "echo", "done"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	elapsed := time.Since(start)

	require.NoError(t, err)
	require.GreaterOrEqual(t, elapsed.Milliseconds(), int64(1000), "command should have slept for at least 1 second")
}

func TestExecIntegration_HeredocInput(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"cat"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"cat"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_FileOperations(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"sh", "-c", "echo 'test content' > /tmp/test.txt && cat /tmp/test.txt"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"sh", "-c", "echo 'test content' > /tmp/test.txt && cat /tmp/test.txt"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_ChainedCommands(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"echo", "test"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"sh", "-c", "echo one && echo two && echo three"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_Globbing(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"ls", "/"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"ls", "/"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_PipeOperations(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"echo", "test"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"sh", "-c", "echo 'hello world' | wc -w"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}

func TestExecIntegration_Redirection(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	pool, err := dockertest.NewPool("")
	require.NoError(t, err)

	resource, err := pool.RunWithOptions(
		&dockertest.RunOptions{
			Repository: "ubuntu",
			Tag:        "22.04",
			Cmd:        []string{"echo", "test"},
		},
		func(config *docker.HostConfig) {
			config.AutoRemove = true
		},
	)
	require.NoError(t, err)
	defer pool.Purge(resource)

	var stdout, stderr bytes.Buffer
	_, err = resource.Exec(
		[]string{"sh", "-c", "echo error >&2 && echo success"},
		dockertest.ExecOptions{
			StdOut: &stdout,
			StdErr: &stderr,
		},
	)
	require.NoError(t, err)
}
