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

// violation records a detected direct cloud-hypervisor reference.
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

func detectViolations(src, filename string) ([]violation, error) {
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

func TestNoDirectCHBinaryRef(t *testing.T) {
	t.Run("self_check_bad_call_detected", func(t *testing.T) {
		src := `package foo
import "os/exec"
func f() { exec.Command("cloud-hypervisor", "--cpus", "1") }
`
		vs, err := detectViolations(src, "fake.go")
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
		vs, err := detectViolations(src, "fake.go")
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
		vs, err := detectViolations(src, "fake.go")
		if err != nil {
			t.Fatalf("detect: %v", err)
		}
		if len(vs) != 0 {
			t.Fatalf("self-check: pgrep example must not trip; got %v", vs)
		}
	})

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	modRoot, err := findModRoot(cwd)
	if err != nil {
		t.Fatalf("find module root: %v", err)
	}
	hostbinRel := filepath.Join("internal", "core", "hostbin")

	err = filepath.WalkDir(modRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			name := d.Name()
			if skipDirs[name] || strings.HasPrefix(name, ".") {
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
		vs, err := detectViolations(string(data), path)
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
