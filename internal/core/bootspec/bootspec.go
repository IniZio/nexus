package bootspec

// Path is the in-guest location of the generic boot manifest the nexus agent
// reads at boot. Writers place it in a built rootfs at <rootfs>/etc/nexus/boot.json.
const Path = "/etc/nexus/boot.json"

// Task is one declared boot command run by the agent as a supervised child.
type Task struct {
	Name       string   `json:"name,omitempty"`       // human label for logs (optional)
	Argv       []string `json:"argv"`                 // command + args; Argv[0] resolved via PATH
	Cwd        string   `json:"cwd,omitempty"`        // working directory; empty => agent default
	Env        []string `json:"env,omitempty"`        // KEY=VALUE pairs; merged over the agent baseline env
	Background bool     `json:"background,omitempty"` // true => run in background, never block PID1; false => run to completion before the next task
}

// Spec is the whole boot manifest.
type Spec struct {
	Tasks []Task `json:"tasks"`
	// Env is the image-wide environment (OCI config Env) as KEY=VALUE pairs.
	// It is captured even when the image declares no process, so the agent
	// can layer it into every exec'd shell. Older manifests omit it.
	Env []string `json:"env,omitempty"`
}

// IsEmpty reports whether the Spec carries nothing worth persisting: no
// boot task and no image environment.
func (s Spec) IsEmpty() bool {
	return len(s.Tasks) == 0 && len(s.Env) == 0
}

// OCIImageConfig is the subset of an OCI image config this package needs.
// Callers adapt github.com/google/go-containerregistry v1.Config or a raw
// image-config JSON into this shape.
type OCIImageConfig struct {
	Entrypoint []string
	Cmd        []string
	WorkingDir string
	Env        []string
}

// FromOCIImageConfig translates an OCI image config into a Spec with a single
// background Task representing the image's declared process. The command is
// Entrypoint followed by Cmd (OCI semantics). WorkingDir maps to Task.Cwd and
// Env to Task.Env. It is marked Background=true because the nexus agent is
// PID 1: a boot command that blocks would prevent the agent from binding its
// control plane, and a PID-1 child that exits must never take the VM down.
//
// Env is always copied to Spec.Env, independent of whether a process is
// declared, so a Containerfile with only ENV still reaches exec shells.
//
// If both Entrypoint and Cmd are empty, no Task is produced: an image with no
// declared process (the pure dev-workspace case) contributes no boot task,
// matching the devcontainers `overrideCommand` behavior.
func FromOCIImageConfig(cfg OCIImageConfig) Spec {
	argv := append(append([]string{}, cfg.Entrypoint...), cfg.Cmd...)
	if len(argv) == 0 {
		return Spec{Env: cfg.Env}
	}
	return Spec{
		Env: cfg.Env,
		Tasks: []Task{
			{
				Argv:       argv,
				Cwd:        cfg.WorkingDir,
				Env:        cfg.Env,
				Background: true,
			},
		},
	}
}
