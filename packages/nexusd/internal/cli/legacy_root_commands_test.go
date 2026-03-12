package cli

import (
	"testing"
)

func TestLegacyRootCommandsRemoved(t *testing.T) {
	rootCommands := map[string]struct{}{}
	for _, cmd := range rootCmd.Commands() {
		rootCommands[cmd.Name()] = struct{}{}
	}

	for _, name := range []string{"workspace", "trace"} {
		t.Run(name+" not registered on root", func(t *testing.T) {
			if _, ok := rootCommands[name]; ok {
				t.Fatalf("expected root command %q to not be registered", name)
			}
		})
	}
}
