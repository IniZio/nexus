package mitm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGraphQLShapeCorpus(t *testing.T) {
	entries := loadCorpus(t)
	if len(entries) == 0 {
		t.Fatal("corpus is empty")
	}

	for _, e := range entries {
		e := e
		name := e.OperationName + "/" + e.Command
		t.Run(name, func(t *testing.T) {
			var body gqlBody
			if err := json.Unmarshal([]byte(e.Body), &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if body.Query == "" {
				t.Fatal("empty query")
			}

			op, err := parseGraphQLOperation(body.Query)
			if err != nil {
				t.Fatalf("parse query: %v", err)
			}

			for _, f := range op.Root {
				shape, ok := githubRootShapes[f.Name]
				if !ok {
					t.Errorf("root field %q not in githubRootShapes", f.Name)
					continue
				}
				allow, path := shapeAllows(shape, f.Children)
				if !allow {
					t.Errorf("shapeAllows denied field %q at path %q", f.Name, path)
				}
			}
		})
	}
}

// TestGraphQLShapeDenials verifies that queries which must be blocked are
// indeed denied, and that the returned path identifies the offending field.
func TestGraphQLShapeDenials(t *testing.T) {
	// helper: parse query, call shapeAllows on the named root field's children.
	// Returns (ok, path) from shapeAllows, or fails the test if parse fails.
	check := func(t *testing.T, query, rootName string) (bool, string) {
		t.Helper()
		op, err := parseGraphQLOperation(query)
		if err != nil {
			t.Fatalf("parse %q: %v", query, err)
		}
		for _, f := range op.Root {
			if f.Name == rootName {
				shape, shapeExists := githubRootShapes[rootName]
				if !shapeExists {
					t.Fatalf("root %q not in githubRootShapes", rootName)
				}
				return shapeAllows(shape, f.Children)
			}
		}
		t.Fatalf("root field %q not found in parsed query", rootName)
		return false, ""
	}

	cases := []struct {
		name         string
		query        string
		rootName     string
		wantDeny     bool
		pathContains string // non-empty: path must contain this substring
	}{
		// ── cross-repo traversal via owner ────────────────────────────────────
		{
			name:         "owner.repositories blocked",
			query:        `{ repository { owner { repositories(first:100) { nodes { name } } } } }`,
			rootName:     "repository",
			wantDeny:     true,
			pathContains: "repositories",
		},
		// ── cross-repo traversal via parent ───────────────────────────────────
		{
			name:         "parent.pullRequests blocked",
			query:        `{ repository { parent { pullRequests(first:1) { nodes { title } } } } }`,
			rootName:     "repository",
			wantDeny:     true,
			pathContains: "pullRequests",
		},
		// ── cross-repo traversal via PR.author inline fragment ─────────────────
		{
			name: "author.repositories blocked (inline fragment on User)",
			query: `{
  repository {
    pullRequest(number: 1) {
      author {
        ... on User { repositories(first:1) { nodes { name } } }
      }
    }
  }
}`,
			rootName:     "repository",
			wantDeny:     true,
			pathContains: "repositories",
		},
		// ── field not in schema at all ────────────────────────────────────────
		{
			name:         "collaborators blocked",
			query:        `{ repository { collaborators { nodes { login } } } }`,
			rootName:     "repository",
			wantDeny:     true,
			pathContains: "collaborators",
		},
		// ── leaf in shape but request adds sub-selection ─────────────────────
		{
			name:         "id is leaf, sub-selection denied",
			query:        `{ repository { id { x } } }`,
			rootName:     "repository",
			wantDeny:     true,
			pathContains: "id",
		},
		// ── non-leaf in shape but request omits sub-selection ────────────────
		{
			name:         "parent without selection denied",
			query:        `{ repository { parent } }`,
			rootName:     "repository",
			wantDeny:     true,
			pathContains: "parent",
		},
		// ── viewer: extra fields ──────────────────────────────────────────────
		{
			name:         "viewer.repositories blocked",
			query:        `{ viewer { login repositories(first:1) { nodes { name } } } }`,
			rootName:     "viewer",
			wantDeny:     true,
			pathContains: "repositories",
		},
		{
			name:         "viewer.email blocked",
			query:        `{ viewer { email } }`,
			rootName:     "viewer",
			wantDeny:     true,
			pathContains: "email",
		},
		{
			name:         "search.nodes.name (Repository type) blocked",
			query:        `{ search { nodes { ... on Repository { name } } } }`,
			rootName:     "search",
			wantDeny:     true,
			pathContains: "name",
		},
		// ── createPullRequest: cross-repo traversal via PR.repository ─────────
		{
			name: "createPullRequest.pullRequest.repository.issues blocked",
			query: `{
  createPullRequest {
    pullRequest {
      repository { issues(first:1) { nodes { title } } }
    }
  }
}`,
			rootName:     "createPullRequest",
			wantDeny:     true,
			pathContains: "repository",
		},
		// ── __type: fields.type not in corpus ────────────────────────────────
		{
			name:         "__type.fields.type blocked",
			query:        `{ __type { fields { name type { name } } } }`,
			rootName:     "__type",
			wantDeny:     true,
			pathContains: "type",
		},
		// ── allow cases ───────────────────────────────────────────────────────
		{
			name:     "repository id allowed (leaf)",
			query:    `{ repository { id } }`,
			rootName: "repository",
			wantDeny: false,
		},
		{
			name:     "viewer login allowed",
			query:    `{ viewer { login } }`,
			rootName: "viewer",
			wantDeny: false,
		},
		{
			name:     "__type fields name allowed",
			query:    `{ __type { fields { name } } }`,
			rootName: "__type",
			wantDeny: false,
		},
		{
			name:     "__type enumValues name allowed",
			query:    `{ __type { enumValues { name } } }`,
			rootName: "__type",
			wantDeny: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ok, path := check(t, tc.query, tc.rootName)
			if tc.wantDeny {
				if ok {
					t.Errorf("expected deny but got allow")
				} else if tc.pathContains != "" && !strings.Contains(path, tc.pathContains) {
					t.Errorf("path %q does not contain %q", path, tc.pathContains)
				}
			} else {
				if !ok {
					t.Errorf("expected allow but got deny at path %q", path)
				}
			}
		})
	}
}

// TestGraphQLShapeTypename verifies the __typename carve-out:
// allowed as a bare leaf in any context, denied if it carries sub-fields.
func TestGraphQLShapeTypename(t *testing.T) {
	parse := func(t *testing.T, q string) *gqlOperation {
		t.Helper()
		op, err := parseGraphQLOperation(q)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return op
	}

	t.Run("__typename allowed at repository root", func(t *testing.T) {
		op := parse(t, `{ repository { __typename } }`)
		shape := githubRootShapes["repository"]
		ok, path := shapeAllows(shape, op.Root[0].Children)
		if !ok {
			t.Errorf("expected allow, got deny at %q", path)
		}
	})

	t.Run("__typename allowed alongside known field", func(t *testing.T) {
		op := parse(t, `{ viewer { login __typename } }`)
		shape := githubRootShapes["viewer"]
		ok, path := shapeAllows(shape, op.Root[0].Children)
		if !ok {
			t.Errorf("expected allow, got deny at %q", path)
		}
	})

	t.Run("__typename allowed inside nested object", func(t *testing.T) {
		op := parse(t, `{ repository { owner { __typename login } } }`)
		shape := githubRootShapes["repository"]
		ok, path := shapeAllows(shape, op.Root[0].Children)
		if !ok {
			t.Errorf("expected allow, got deny at %q", path)
		}
	})

	t.Run("__typename with children denied", func(t *testing.T) {
		op := parse(t, `{ viewer { __typename { email } } }`)
		shape := githubRootShapes["viewer"]
		ok, path := shapeAllows(shape, op.Root[0].Children)
		if ok {
			t.Error("expected deny for __typename with children, got allow")
		}
		if !strings.Contains(path, "__typename") {
			t.Errorf("expected path to contain __typename, got %q", path)
		}
	})
}
