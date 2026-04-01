package firecracker

type LimaBridge struct {
	instance string
}

func NewLimaBridge(instance string) *LimaBridge {
	if instance == "" {
		instance = "nexus-firecracker"
	}
	return &LimaBridge{instance: instance}
}

func (b *LimaBridge) Wrap(cmd string, args ...string) (string, []string) {
	wrapped := make([]string, 0, len(args)+3)
	wrapped = append(wrapped, "shell", b.instance, cmd)
	wrapped = append(wrapped, args...)
	return "limactl", wrapped
}
