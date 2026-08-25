package bootspec

import (
	"testing"
)

func TestFromOCIImageConfig_NoInputMutation(t *testing.T) {
	// ep has spare capacity so a naive append(ep, cmd...) would write into the
	// backing array and mutate the caller's slice.
	ep := make([]string, 1, 4)
	ep[0] = "/bin/echo"
	cmd := []string{"hello", "world"}

	_ = FromOCIImageConfig(OCIImageConfig{Entrypoint: ep, Cmd: cmd})

	// len must still be 1.
	if len(ep) != 1 {
		t.Fatalf("Entrypoint slice len mutated: got %d, want 1", len(ep))
	}
	// Expand to full capacity and verify cmd values did NOT leak in.
	full := ep[:cap(ep)]
	for i := 1; i < len(full); i++ {
		if full[i] != "" {
			t.Errorf("Entrypoint backing array index %d leaked Cmd value %q", i, full[i])
		}
	}
}

func TestFromOCIImageConfig(t *testing.T) {
	cases := []struct {
		name      string
		cfg       OCIImageConfig
		wantTasks int
		wantArgv  []string
		wantBg    bool
		wantCwd   string
		wantEnv   []string
	}{
		{
			name: "entrypoint+cmd combined",
			cfg: OCIImageConfig{
				Entrypoint: []string{"/usr/bin/dockerd"},
				Cmd:        []string{"--storage-driver=overlay2"},
			},
			wantTasks: 1,
			wantArgv:  []string{"/usr/bin/dockerd", "--storage-driver=overlay2"},
			wantBg:    true,
		},
		{
			name: "entrypoint only",
			cfg: OCIImageConfig{
				Entrypoint: []string{"/usr/bin/dockerd", "--host=unix:///var/run/docker.sock"},
			},
			wantTasks: 1,
			wantArgv:  []string{"/usr/bin/dockerd", "--host=unix:///var/run/docker.sock"},
			wantBg:    true,
		},
		{
			name: "cmd only",
			cfg: OCIImageConfig{
				Cmd: []string{"python3", "-m", "http.server"},
			},
			wantTasks: 1,
			wantArgv:  []string{"python3", "-m", "http.server"},
			wantBg:    true,
		},
		{
			name:      "both empty => no boot task",
			cfg:       OCIImageConfig{},
			wantTasks: 0,
		},
		{
			name: "workingdir and env propagated",
			cfg: OCIImageConfig{
				Entrypoint: []string{"/app/server"},
				WorkingDir: "/app",
				Env:        []string{"PORT=8080", "DEBUG=1"},
			},
			wantTasks: 1,
			wantArgv:  []string{"/app/server"},
			wantBg:    true,
			wantCwd:   "/app",
			wantEnv:   []string{"PORT=8080", "DEBUG=1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := FromOCIImageConfig(tc.cfg)
			if len(spec.Tasks) != tc.wantTasks {
				t.Fatalf("Tasks len = %d, want %d", len(spec.Tasks), tc.wantTasks)
			}
			if tc.wantTasks == 0 {
				return
			}
			task := spec.Tasks[0]
			if len(task.Argv) != len(tc.wantArgv) {
				t.Fatalf("Argv = %v, want %v", task.Argv, tc.wantArgv)
			}
			for i, a := range tc.wantArgv {
				if task.Argv[i] != a {
					t.Errorf("Argv[%d] = %q, want %q", i, task.Argv[i], a)
				}
			}
			if task.Background != tc.wantBg {
				t.Errorf("Background = %v, want %v", task.Background, tc.wantBg)
			}
			if task.Cwd != tc.wantCwd {
				t.Errorf("Cwd = %q, want %q", task.Cwd, tc.wantCwd)
			}
			if len(task.Env) != len(tc.wantEnv) {
				t.Fatalf("Env = %v, want %v", task.Env, tc.wantEnv)
			}
			for i, e := range tc.wantEnv {
				if task.Env[i] != e {
					t.Errorf("Env[%d] = %q, want %q", i, task.Env[i], e)
				}
			}
		})
	}
}
