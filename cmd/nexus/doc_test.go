package main

import (
	"testing"
)

func TestDocCommandsRegistered(t *testing.T) {
	docCommands := []string{"create", "verify", "assign", "publish", "list"}

	for _, cmd := range docCommands {
		t.Run("doc "+cmd, func(t *testing.T) {
			found := false
			for _, c := range docCmd.Commands() {
				if c.Name() == cmd {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("doc %s command not registered", cmd)
			}
		})
	}
}

func TestDocCmdHasParent(t *testing.T) {
	if docCmd == nil {
		t.Fatal("docCmd is nil")
	}
	if docCmd.Parent() == nil {
		t.Error("docCmd has no parent rootCmd")
	}
	if docCmd.Parent().Name() != "nexus" {
		t.Errorf("docCmd parent name is %s, expected 'nexus'", docCmd.Parent().Name())
	}
}
