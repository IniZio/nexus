package driver_test

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const chImportPath = "github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"

// Linux-only tools that may import the CH package without a build tag.
var chImportAllowDirs = []string{
	"cmd/buildorca",
	"internal/test/netnsreentryproof",
}

// Darwin builds must not depend on the CH package: outside the CH package and
// backends/, importers must be linux-constrained.
func TestCloudHypervisorImportedOnlyFromLinuxFiles(t *testing.T) {
	root := archModRoot(t)
	var bad []string
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() {
				if d.Name() == "testdata" || d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if chImportExempt(rel) {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly|parser.ParseComments)
			if err != nil {
				t.Errorf("parse %s: %v", rel, err)
				return nil
			}
			imports := false
			for _, imp := range f.Imports {
				if p, _ := strconv.Unquote(imp.Path.Value); p == chImportPath {
					imports = true
				}
			}
			if !imports {
				return nil
			}
			if strings.HasSuffix(strings.TrimSuffix(rel, ".go"), "_linux") || linuxOnlyConstraint(f.Comments, f.Package) {
				return nil
			}
			bad = append(bad, rel)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
	for _, b := range bad {
		t.Errorf("%s imports %s without a linux build constraint", b, chImportPath)
	}
}

func chImportExempt(rel string) bool {
	for _, d := range append([]string{"internal/core/driver/cloudhypervisor", "internal/core/driver/backends"}, chImportAllowDirs...) {
		if strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// linuxOnlyConstraint reports whether a //go:build line before the package
// clause is satisfied on linux and not on darwin.
func linuxOnlyConstraint(groups []*ast.CommentGroup, pkgPos token.Pos) bool {
	for _, g := range groups {
		if g.Pos() >= pkgPos {
			break
		}
		for _, c := range g.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				return false
			}
			on := func(goos string) bool {
				return expr.Eval(func(tag string) bool {
					return tag == goos || (goos == "linux" && tag == "unix") || (goos == "darwin" && tag == "unix") || tag == "s9blive"
				})
			}
			return on("linux") && !on("darwin")
		}
	}
	return false
}

func archModRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
