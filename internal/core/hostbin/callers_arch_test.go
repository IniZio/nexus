package hostbin_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// violation records a detected direct binary reference.
type violation struct {
	file string
	line int
	msg  string
}

// isCHArg returns true if expr is the string literal "cloud-hypervisor"
// or a selector whose ident is CloudHypervisor (e.g. hostbin.CloudHypervisor).
func isCHArg(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING && v.Value == `"cloud-hypervisor"`
	case *ast.SelectorExpr:
		return v.Sel.Name == "CloudHypervisor"
	}
	return false
}

// isNexusAgentArg returns true if expr is the string literal "nexus-agent"
// or a selector whose ident is NexusAgent (e.g. hostbin.NexusAgent).
func isNexusAgentArg(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING && v.Value == `"nexus-agent"`
	case *ast.SelectorExpr:
		return v.Sel.Name == "NexusAgent"
	}
	return false
}

func detectCHViolations(src, filename string) ([]violation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", filename, err)
	}
	var vs []violation
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pos := fset.Position(call.Pos())
		switch sel.Sel.Name {
		case "LookPath":
			if len(call.Args) >= 1 && isCHArg(call.Args[0]) {
				vs = append(vs, violation{pos.Filename, pos.Line, "LookPath(cloud-hypervisor)"})
			}
		case "Command":
			if len(call.Args) >= 1 && isCHArg(call.Args[0]) {
				vs = append(vs, violation{pos.Filename, pos.Line, "Command(cloud-hypervisor)"})
			}
		case "CommandContext":
			if len(call.Args) >= 2 && isCHArg(call.Args[1]) {
				vs = append(vs, violation{pos.Filename, pos.Line, "CommandContext(ctx,cloud-hypervisor)"})
			}
		}
		return true
	})
	return vs, nil
}

func detectNexusAgentViolations(src, filename string) ([]violation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", filename, err)
	}
	var vs []violation
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "LookPath" && len(call.Args) >= 1 && isNexusAgentArg(call.Args[0]) {
			pos := fset.Position(call.Pos())
			vs = append(vs, violation{pos.Filename, pos.Line, "LookPath(nexus-agent)"})
		}
		return true
	})
	return vs, nil
}

func findModRoot(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", start)
		}
		dir = parent
	}
}

// skipDirs are directory names that are never descended into.
var skipDirs = map[string]bool{
	"vendor":       true,
	"testdata":     true,
	"node_modules": true,
}

// walkSourceFiles visits every non-test .go file in modRoot, skipping
// hostbinRel, vendor, testdata, dot-dirs, and the top-level doc/ tree (docs,
// not product code).
func walkSourceFiles(modRoot, hostbinRel string, fn func(path string, data []byte) error) error {
	return filepath.WalkDir(modRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			name := d.Name()
			if skipDirs[name] || strings.HasPrefix(name, ".") || (name == "doc" && filepath.Dir(path) == modRoot) {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(modRoot, path)
			if rel == hostbinRel {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		return fn(path, data)
	})
}

func testModRoot(t *testing.T) (modRoot, hostbinRel string) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	modRoot, err = findModRoot(cwd)
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}
	hostbinRel = filepath.Join("internal", "core", "hostbin")
	return
}

func TestNoDirectCHBinaryRef(t *testing.T) {
	t.Run("self_check_bad_call_detected", func(t *testing.T) {
		src := `package foo
import "os/exec"
func f() { exec.Command("cloud-hypervisor", "--cpus", "1") }
`
		vs, err := detectCHViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) == 0 {
			t.Fatal("self-check: expected violation for exec.Command(\"cloud-hypervisor\"), got none")
		}
	})

	t.Run("self_check_lookpath_ident_detected", func(t *testing.T) {
		src := `package foo
func f() { p, _ := exec.LookPath(hostbin.CloudHypervisor); _ = p }
`
		vs, err := detectCHViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) == 0 {
			t.Fatal("self-check: expected violation for LookPath(hostbin.CloudHypervisor), got none")
		}
	})

	t.Run("self_check_pgrep_not_detected", func(t *testing.T) {
		src := `package foo
import "os/exec"
func f() { exec.Command("pgrep", "-fa", "cloud-hypervisor") }
`
		vs, err := detectCHViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) != 0 {
			t.Fatalf("self-check: pgrep example must not trip; got %v", vs)
		}
	})

	modRoot, hostbinRel := testModRoot(t)
	err := walkSourceFiles(modRoot, hostbinRel, func(path string, data []byte) error {
		vs, err := detectCHViolations(string(data), path)
		if err != nil {
			return err
		}
		for _, v := range vs {
			t.Errorf("%s:%d: direct cloud-hypervisor binary ref (%s); use hostbin.Acquire instead",
				v.file, v.line, v.msg)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func isVirtiofsdArg(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING && v.Value == `"virtiofsd"`
	case *ast.SelectorExpr:
		return v.Sel.Name == "Virtiofsd"
	}
	return false
}

func detectVirtiofsdViolations(src, filename string) ([]violation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", filename, err)
	}
	var vs []violation
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "LookPath" && len(call.Args) >= 1 && isVirtiofsdArg(call.Args[0]) {
			pos := fset.Position(call.Pos())
			vs = append(vs, violation{pos.Filename, pos.Line, "LookPath(virtiofsd)"})
		}
		return true
	})
	return vs, nil
}

func TestNoDirectVirtiofsdLookPath(t *testing.T) {
	t.Run("self_check_literal_detected", func(t *testing.T) {
		src := `package foo
import "os/exec"
func f() { p, _ := exec.LookPath("virtiofsd"); _ = p }
`
		vs, err := detectVirtiofsdViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) == 0 {
			t.Fatal("self-check: expected violation for exec.LookPath(\"virtiofsd\"), got none")
		}
	})

	modRoot, hostbinRel := testModRoot(t)
	err := walkSourceFiles(modRoot, hostbinRel, func(path string, data []byte) error {
		vs, err := detectVirtiofsdViolations(string(data), path)
		if err != nil {
			return err
		}
		for _, v := range vs {
			t.Errorf("%s:%d: direct virtiofsd LookPath (%s); use hostbin.Resolve instead",
				v.file, v.line, v.msg)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func TestNoDirectNexusAgentLookPath(t *testing.T) {
	t.Run("self_check_literal_detected", func(t *testing.T) {
		src := `package foo
import "os/exec"
func f() { p, _ := exec.LookPath("nexus-agent"); _ = p }
`
		vs, err := detectNexusAgentViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) == 0 {
			t.Fatal("self-check: expected violation for exec.LookPath(\"nexus-agent\"), got none")
		}
	})

	t.Run("self_check_selector_detected", func(t *testing.T) {
		src := `package foo
func f() { p, _ := exec.LookPath(hostbin.NexusAgent); _ = p }
`
		vs, err := detectNexusAgentViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) == 0 {
			t.Fatal("self-check: expected violation for exec.LookPath(hostbin.NexusAgent), got none")
		}
	})

	t.Run("self_check_filepath_join_not_detected", func(t *testing.T) {
		src := `package foo
import "path/filepath"
func f() { p := filepath.Join(dir, "nexus-agent"); _ = p }
`
		vs, err := detectNexusAgentViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) != 0 {
			t.Fatalf("self-check: filepath.Join must not trip; got %v", vs)
		}
	})

	modRoot, hostbinRel := testModRoot(t)
	err := walkSourceFiles(modRoot, hostbinRel, func(path string, data []byte) error {
		vs, err := detectNexusAgentViolations(string(data), path)
		if err != nil {
			return err
		}
		for _, v := range vs {
			t.Errorf("%s:%d: direct nexus-agent LookPath (%s); use hostbin.ResolveAgent instead",
				v.file, v.line, v.msg)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// isE2fsprogsArg returns true if expr is one of the e2fsprogs binary string
// literals or a hostbin selector constant for those binaries.
func isE2fsprogsArg(expr ast.Expr) bool {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return false
		}
		switch v.Value {
		case `"mke2fs"`, `"e2fsck"`, `"resize2fs"`, `"mkfs.ext4"`:
			return true
		}
	case *ast.SelectorExpr:
		switch v.Sel.Name {
		case "Mke2fs", "E2fsck", "Resize2fs":
			return true
		}
	}
	return false
}

func detectE2fsprogsViolations(src, filename string) ([]violation, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", filename, err)
	}
	var vs []violation
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pos := fset.Position(call.Pos())
		switch sel.Sel.Name {
		case "LookPath":
			if len(call.Args) >= 1 && isE2fsprogsArg(call.Args[0]) {
				vs = append(vs, violation{pos.Filename, pos.Line, "LookPath(e2fsprogs)"})
			}
		case "Command":
			if len(call.Args) >= 1 && isE2fsprogsArg(call.Args[0]) {
				vs = append(vs, violation{pos.Filename, pos.Line, "Command(e2fsprogs)"})
			}
		case "CommandContext":
			if len(call.Args) >= 2 && isE2fsprogsArg(call.Args[1]) {
				vs = append(vs, violation{pos.Filename, pos.Line, "CommandContext(ctx,e2fsprogs)"})
			}
		}
		return true
	})
	return vs, nil
}

// e2fsprogsGuestExemptions lists source subtrees that run inside a VM guest
// where PATH resolution is the guest OS's responsibility, not host PATH.
// Format: slash-separated paths relative to module root.
var e2fsprogsGuestExemptions = []string{
	// nexus-agent is PID 1 in the VM; the kernel provides no PATH.
	"cmd/nexus-agent",
	// buildkit_linux.go runs inside the builder VM guest, not on the host.
	"internal/core/agent",
}

func TestNoDirectE2fsprogsLookPath(t *testing.T) {
	t.Run("self_check_mke2fs_literal_detected", func(t *testing.T) {
		src := `package foo
import "os/exec"
func f() { p, _ := exec.LookPath("mke2fs"); _ = p }
`
		vs, err := detectE2fsprogsViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) == 0 {
			t.Fatal("self-check: expected violation for exec.LookPath(\"mke2fs\"), got none")
		}
	})

	t.Run("self_check_resize2fs_command_detected", func(t *testing.T) {
		src := `package foo
import "os/exec"
func f() { exec.Command("resize2fs", "-f", dev) }
`
		vs, err := detectE2fsprogsViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) == 0 {
			t.Fatal("self-check: expected violation for exec.Command(\"resize2fs\"), got none")
		}
	})

	t.Run("self_check_mkfs_ext4_command_context_detected", func(t *testing.T) {
		src := `package foo
import "os/exec"
func f(ctx context.Context) { exec.CommandContext(ctx, "mkfs.ext4", "-F", dev) }
`
		vs, err := detectE2fsprogsViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) == 0 {
			t.Fatal("self-check: expected violation for exec.CommandContext(ctx, \"mkfs.ext4\"), got none")
		}
	})

	t.Run("self_check_selector_detected", func(t *testing.T) {
		src := `package foo
func f() { exec.Command(hostbin.Mke2fs, "-d", srcDir, imgPath) }
`
		vs, err := detectE2fsprogsViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) == 0 {
			t.Fatal("self-check: expected violation for exec.Command(hostbin.Mke2fs), got none")
		}
	})

	t.Run("self_check_variable_path_not_detected", func(t *testing.T) {
		src := `package foo
import "os/exec"
func f(path string) { exec.Command(path, "-d", srcDir, imgPath) }
`
		vs, err := detectE2fsprogsViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) != 0 {
			t.Fatalf("self-check: variable-path exec.Command must not trip; got %v", vs)
		}
	})

	t.Run("self_check_other_binary_not_detected", func(t *testing.T) {
		src := `package foo
import "os/exec"
func f() { exec.Command("blkid", "-o", "value", "-s", "TYPE", device) }
`
		vs, err := detectE2fsprogsViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) != 0 {
			t.Fatalf("self-check: unrelated binary must not trip; got %v", vs)
		}
	})

	modRoot, hostbinRel := testModRoot(t)
	err := walkSourceFiles(modRoot, hostbinRel, func(path string, data []byte) error {
		rel, _ := filepath.Rel(modRoot, path)
		relSlash := filepath.ToSlash(rel)
		for _, exempt := range e2fsprogsGuestExemptions {
			if strings.HasPrefix(relSlash, exempt+"/") || relSlash == exempt {
				return nil
			}
		}
		vs, err := detectE2fsprogsViolations(string(data), path)
		if err != nil {
			return err
		}
		for _, v := range vs {
			t.Errorf("%s:%d: direct e2fsprogs binary ref (%s); use hostbin.Acquire instead",
				v.file, v.line, v.msg)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
