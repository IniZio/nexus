package cred_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoConcreteProfileVars(t *testing.T) {
	forbidden := []string{
		"ClaudeCodeProfile",
		"CursorAgentProfile",
		"OpencodeProfile",
		"OhMyPiProfile",
		"KiroProfile",
		"CodexProfile",
	}
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %q: %v", path, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			for _, name := range forbidden {
				if ident.Name == name {
					pos := fset.Position(ident.Pos())
					t.Errorf("production file %s:%d references concrete profile var %q; use ProfileByName instead",
						pos.Filename, pos.Line, name)
				}
			}
			return true
		})
	}
}
