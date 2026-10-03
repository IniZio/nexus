package hostbin_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type execSite struct {
	dir     string
	file    string
	line    int
	name    string
	literal bool
}

var bundledTools = map[string]bool{
	"cloud-hypervisor": true,
	"virtiofsd":        true,
	"mke2fs":           true,
	"e2fsck":           true,
	"resize2fs":        true,
	"mkfs.ext4":        true,
}

// hostExecAllowlist maps a host program name to the package dirs (relative
// to the module root) permitted to exec it, each with a reason.
var hostExecAllowlist = map[string]map[string]string{
	"claude": {
		"internal/cli": "claude CLI version probe (claude-mod check)",
	},
	"cp": {
		"internal/test/repro":         "reflink/file copy",
		"internal/testutil/livenexus": "reflink/file copy",
	},
	"debugfs": {
		"internal/test/repro": "test tooling",
	},
	"dhclient": {
		"third_party/gvisor-tap-vsock/cmd/vm": "vendored third_party guest helper",
	},
	"docker": {
		"internal/test/selfhost": "selfhost test tooling",
	},
	"fallocate": {
		"internal/test/repro": "test tooling",
	},
	"free": {
		"internal/test/repro": "test tooling",
	},
	"git": {
		"internal/cli":                      "git worktree/clone integration (optional)",
		"internal/controller/backend/herdr": "git worktree/clone integration (optional)",
		"internal/core/service":             "git worktree/clone integration (optional)",
		"internal/test/selfhost":            "git worktree/clone integration (optional)",
		"internal/testutil/livenexus":       "git worktree/clone integration (optional)",
	},
	"go": {
		"internal/core/hostbin/internal/genartifacts": "go toolchain (dev/test tooling)",
		"internal/test/repro":                         "go toolchain (dev/test tooling)",
		"internal/test/selfhost":                      "go toolchain (dev/test tooling)",
		"internal/testutil/livenexus":                 "go toolchain (dev/test tooling)",
	},
	"herdr": {
		"internal/cli":                      "herdr integration (optional)",
		"internal/clientagent":              "herdr integration (optional)",
		"internal/controller/backend/herdr": "herdr integration (optional)",
		"internal/herdrworktree":            "herdr integration (optional)",
		"internal/mcp":                      "herdr integration (optional)",
		"internal/supervisor":               "delegate pane watch (optional)",
		"internal/testutil/livenexus":       "herdr integration (optional)",
	},
	"lsof": {
		"internal/cli": "port discovery",
	},
	"nexus": {
		"internal/controller/backend/herdr": "nexus CLI self-invocation",
		"internal/controller/sandbox":       "nexus CLI self-invocation",
		"internal/testutil/livenexus":       "nexus CLI self-invocation",
	},
	"nexus-controller": {
		"internal/cli": "controller binary launch",
	},
	"pgrep": {
		"internal/test/repro": "test process lookup",
	},
	"ps": {
		"internal/cli":         "process inspection",
		"internal/clientagent": "process inspection",
	},
	"sha256sum": {
		"internal/test/repro": "test tooling",
	},
	"ss": {
		"internal/cli": "port discovery",
	},
	"ssh": {
		"internal/cli": "git ssh relay",
	},
	"ssh-add": {
		"internal/core/gitssh": "ssh-agent key check",
	},
	"ssh-keygen": {
		"third_party/gvisor-tap-vsock/test-utils": "vendored third_party test util",
	},
	"stress-ng": {
		"internal/test/repro": "test tooling",
	},
	"systemctl": {
		"internal/supervisor":         "user unit control (optional)",
		"internal/testutil/livenexus": "user unit control (optional)",
	},
	"systemd-run": {
		"internal/supervisor":         "cgroup scope (optional)",
		"internal/testutil/livenexus": "cgroup scope (optional)",
	},
	"udhcpc": {
		"third_party/gvisor-tap-vsock/cmd/vm": "vendored third_party guest helper",
	},
	"xzcat": {
		"third_party/gvisor-tap-vsock/test-utils": "vendored third_party test util",
	},
}

var hostExecGuestDirs = []string{"cmd/nexus-agent", "internal/core/agent"}

func stringConsts(f *ast.File, into map[string]string) {
	ast.Inspect(f, func(n ast.Node) bool {
		gd, ok := n.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			return true
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, id := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						into[id.Name] = s
					}
				}
			}
		}
		return true
	})
}

func resolveProgram(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			if s, err := strconv.Unquote(v.Value); err == nil {
				return s, true
			}
		}
	case *ast.Ident:
		if s, ok := consts[v.Name]; ok {
			return s, true
		}
	}
	return "", false
}

// scanExecSites walks root and returns every exec.LookPath/Command/
// CommandContext call in non-test files, skipping skip(relDir) dirs.
func scanExecSites(t *testing.T, root string, skip func(rel string) bool) []execSite {
	t.Helper()
	byDir := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (skipDirs[name] || strings.HasPrefix(name, ".") || (name == "doc" && filepath.Dir(path) == root)) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			byDir[filepath.Dir(path)] = append(byDir[filepath.Dir(path)], path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	var sites []execSite
	for dir, files := range byDir {
		rel, _ := filepath.Rel(root, dir)
		rel = filepath.ToSlash(rel)
		if skip != nil && skip(rel) {
			continue
		}
		fset := token.NewFileSet()
		consts := map[string]string{}
		var parsed []*ast.File
		for _, p := range files {
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", p, err)
			}
			stringConsts(f, consts)
			parsed = append(parsed, f)
		}
		for _, f := range parsed {
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "exec" {
					return true
				}
				idx := 0
				switch sel.Sel.Name {
				case "LookPath", "Command":
				case "CommandContext":
					idx = 1
				default:
					return true
				}
				if len(call.Args) <= idx {
					return true
				}
				pos := fset.Position(call.Pos())
				name, lit := resolveProgram(call.Args[idx], consts)
				sites = append(sites, execSite{rel, filepath.Base(pos.Filename), pos.Line, name, lit})
				return true
			})
		}
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].dir != sites[j].dir {
			return sites[i].dir < sites[j].dir
		}
		if sites[i].file != sites[j].file {
			return sites[i].file < sites[j].file
		}
		return sites[i].line < sites[j].line
	})
	return sites
}

func checkExecSites(sites []execSite, allow map[string]map[string]string) (bad []string, nonLiteral int) {
	for _, s := range sites {
		if !s.literal {
			nonLiteral++
			continue
		}
		if bundledTools[s.name] {
			bad = append(bad, s.dir+"/"+s.file+":"+strconv.Itoa(s.line)+": bundled tool "+strconv.Quote(s.name)+" exec'd directly; use hostbin")
			continue
		}
		if _, ok := allow[s.name][s.dir]; !ok {
			bad = append(bad, s.dir+"/"+s.file+":"+strconv.Itoa(s.line)+": "+strconv.Quote(s.name)+" not allowlisted for package "+s.dir)
		}
	}
	return
}

func TestHostExecAllowlist(t *testing.T) {
	modRoot, _ := testModRoot(t)
	sites := scanExecSites(t, modRoot, func(rel string) bool {
		for _, g := range hostExecGuestDirs {
			if rel == g || strings.HasPrefix(rel, g+"/") {
				return true
			}
		}
		return rel == "internal/core/hostbin"
	})
	if os.Getenv("Z8B_DUMP") != "" {
		seen := map[string]bool{}
		for _, s := range sites {
			if s.literal {
				k := s.name + "\t" + s.dir
				if !seen[k] {
					seen[k] = true
					t.Logf("SITE\t%s", k)
				}
			}
		}
	}
	bad, nonLiteral := checkExecSites(sites, hostExecAllowlist)
	t.Logf("non-literal exec program args (not enforced): %d", nonLiteral)
	for _, b := range bad {
		t.Error(b)
	}
	for name, dirs := range hostExecAllowlist {
		for dir, reason := range dirs {
			if reason == "" {
				t.Errorf("allowlist %s/%s: empty reason", name, dir)
			}
		}
	}
}

func TestHostExecScannerRejectsFixture(t *testing.T) {
	sites := scanExecSites(t, filepath.Join("testdata", "hostexec"), nil)
	bad, nonLiteral := checkExecSites(sites, map[string]map[string]string{"ok-tool": {"pkg": "fixture"}})
	if len(bad) != 3 {
		t.Fatalf("want 3 violations (bundled literal, unlisted const, unlisted literal), got %d: %v", len(bad), bad)
	}
	if nonLiteral != 1 {
		t.Fatalf("want 1 non-literal site, got %d", nonLiteral)
	}
}
