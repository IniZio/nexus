package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRootCommandSurface(t *testing.T) {
	commandNames := make(map[string]struct{})

	for _, cmd := range rootCmd.Commands() {
		commandNames[cmd.Name()] = struct{}{}
	}

	required := []string{"project", "branch", "version", "environment"}
	for _, name := range required {
		if _, ok := commandNames[name]; !ok {
			t.Fatalf("expected root command %q to be registered", name)
		}
	}

	if _, ok := commandNames["trace"]; ok {
		t.Fatal("expected root command \"trace\" to not be registered")
	}
}

func TestVersionCommandSplit(t *testing.T) {
	productVersionCmd := mustFindRootCommand(t, "version")
	cliVersionCmd := mustFindRootCommand(t, "cli-version")

	if productVersionCmd.Run != nil || productVersionCmd.RunE != nil {
		t.Fatal("expected version command to be reserved for product workflows")
	}

	originalJSONOutput := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = originalJSONOutput })

	output := captureStdout(t, func() {
		if cliVersionCmd.RunE != nil {
			if err := cliVersionCmd.RunE(cliVersionCmd, nil); err != nil {
				t.Fatalf("expected cli-version command to execute: %v", err)
			}
			return
		}

		if cliVersionCmd.Run == nil {
			t.Fatal("expected cli-version command to have a runnable handler")
		}

		cliVersionCmd.Run(cliVersionCmd, nil)
	})

	if !strings.Contains(output, "nexus cli version ") {
		t.Fatalf("expected cli-version output, got %q", output)
	}
}

func TestEnvironmentCommandSurface(t *testing.T) {
	environment := mustFindRootCommand(t, "environment")

	subcommands := make(map[string]*cobra.Command)
	for _, cmd := range environment.Commands() {
		subcommands[cmd.Name()] = cmd
	}

	required := []string{
		"create",
		"delete",
		"list",
		"status",
		"start",
		"stop",
		"exec",
		"ssh",
		"use",
		"logs",
		"checkpoint",
		"inject-key",
	}

	for _, name := range required {
		cmd, ok := subcommands[name]
		if !ok {
			t.Fatalf("expected environment command %q to be registered", name)
		}

		if cmd.Parent() == nil || cmd.Parent().Name() != "environment" {
			t.Fatalf("expected %q parent to be environment command", name)
		}
	}
}

func TestEnvironmentHelpTerminology(t *testing.T) {
	environment := mustFindRootCommand(t, "environment")

	assertHelpTerm := func(commandPath []string, expectedTerm string) {
		t.Helper()

		cmd, _, err := environment.Find(commandPath)
		if err != nil {
			t.Fatalf("expected environment command %q to exist: %v", strings.Join(commandPath, " "), err)
		}

		if !strings.Contains(strings.ToLower(cmd.Short), expectedTerm) {
			t.Fatalf("expected %q short help to contain %q, got %q", strings.Join(commandPath, " "), expectedTerm, cmd.Short)
		}
	}

	assertHelpTerm([]string{"create"}, "environment")
	assertHelpTerm([]string{"list"}, "environment")
	assertHelpTerm([]string{"start"}, "environment")
	assertHelpTerm([]string{"stop"}, "environment")
	assertHelpTerm([]string{"delete"}, "environment")
}

func TestVersionHistoryNotExposed(t *testing.T) {
	version := mustFindRootCommand(t, "version")
	history, _, err := version.Find([]string{"history"})
	if err == nil && history != nil && history.Name() == "history" {
		t.Fatal("expected version command to not expose history subcommand")
	}
}

func TestProjectAndBranchScaffolds(t *testing.T) {
	commands := map[string]*cobra.Command{}
	for _, cmd := range rootCmd.Commands() {
		commands[cmd.Name()] = cmd
	}

	project, ok := commands["project"]
	if !ok {
		t.Fatal("expected root command \"project\" to be registered")
	}

	branch, ok := commands["branch"]
	if !ok {
		t.Fatal("expected root command \"branch\" to be registered")
	}

	projectList, _, err := project.Find([]string{"list"})
	if err != nil {
		t.Fatalf("expected project list subcommand to exist: %v", err)
	}
	if projectList == nil || projectList.Name() != "list" {
		t.Fatal("expected project to expose actionable subcommand \"list\"")
	}
	if projectList.Args == nil {
		t.Fatal("expected project list to define argument validation")
	}

	err = projectList.Args(projectList, []string{"extra"})
	if err == nil {
		t.Fatal("expected project list to reject extra arguments")
	}

	err = invokeCommand(projectList, []string{})
	if err == nil {
		t.Fatal("expected project list scaffold to return a non-zero error")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatal("expected project list scaffold failure to return an actionable error message")
	}

	branchUse, _, err := branch.Find([]string{"use"})
	if err != nil {
		t.Fatalf("expected branch use subcommand to exist: %v", err)
	}
	if branchUse == nil || branchUse.Name() != "use" {
		t.Fatal("expected branch to expose actionable subcommand \"use\"")
	}

	err = branchUse.Args(branchUse, []string{})
	if err == nil {
		t.Fatal("expected branch use to reject missing branch argument")
	}

	err = branchUse.Args(branchUse, []string{"feature/test", "extra"})
	if err == nil {
		t.Fatal("expected branch use to reject extra arguments")
	}

	err = invokeCommand(branchUse, []string{"feature/test"})
	if err == nil {
		t.Fatal("expected branch use scaffold to return a non-zero error")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatal("expected branch use scaffold failure to return an actionable error message")
	}
}

func invokeCommand(cmd *cobra.Command, args []string) error {
	if cmd.RunE != nil {
		return cmd.RunE(cmd, args)
	}

	if cmd.Run != nil {
		cmd.Run(cmd, args)
		return nil
	}

	return nil
}

func mustFindRootCommand(t *testing.T, name string) *cobra.Command {
	t.Helper()

	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == name {
			return cmd
		}
	}

	t.Fatalf("expected root command %q to be registered", name)
	return nil
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed creating stdout pipe: %v", err)
	}

	os.Stdout = writer
	defer func() {
		os.Stdout = originalStdout
		_ = writer.Close()
		_ = reader.Close()
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close stdout writer: %v", err)
	}
	os.Stdout = originalStdout

	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read stdout: %v", err)
	}

	if err := reader.Close(); err != nil {
		t.Fatalf("failed to close stdout reader: %v", err)
	}

	return string(out)
}
