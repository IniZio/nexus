package mitm

import (
	"encoding/json"
	"os"
	"testing"
)

type corpusEntry struct {
	Command       string `json:"command"`
	OperationName string `json:"operationName"`
	Body          string `json:"body"`
}

type gqlBody struct {
	Query         string          `json:"query"`
	Variables     json.RawMessage `json:"variables"`
	OperationName string          `json:"operationName"`
}

func loadCorpus(t testing.TB) []corpusEntry {
	t.Helper()
	data, err := os.ReadFile("testdata/gh_graphql_corpus.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var entries []corpusEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	return entries
}

func TestParseGraphQLCorpus(t *testing.T) {
	entries := loadCorpus(t)
	for _, e := range entries {
		e := e
		t.Run(e.OperationName+"_"+e.Command, func(t *testing.T) {
			var body gqlBody
			if err := json.Unmarshal([]byte(e.Body), &body); err != nil {
				t.Fatalf("parse body json: %v", err)
			}
			op, err := parseGraphQLOperation(body.Query)
			if err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			if op == nil {
				t.Fatal("nil operation")
			}
		})
	}
}

func findField(fields []*gqlField, name string) *gqlField {
	for _, f := range fields {
		if f.ResponseKey() == name {
			return f
		}
	}
	return nil
}

func TestParseGraphQLSpotChecks(t *testing.T) {
	entries := loadCorpus(t)
	byOp := map[string]string{}
	for _, e := range entries {
		if _, dup := byOp[e.OperationName]; !dup {
			var body gqlBody
			if err := json.Unmarshal([]byte(e.Body), &body); err == nil {
				byOp[e.OperationName] = body.Query
			}
		}
	}

	t.Run("RepositoryInfo_fragment_inlined", func(t *testing.T) {
		q, ok := byOp["RepositoryInfo"]
		if !ok {
			t.Skip("RepositoryInfo not in corpus")
		}
		op, err := parseGraphQLOperation(q)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(op.Root) != 1 || op.Root[0].Name != "repository" {
			t.Fatalf("expected root=[repository] got %v", op.Root)
		}
		repo := op.Root[0]
		mustHave := []string{"id", "name", "owner", "parent"}
		for _, name := range mustHave {
			if findField(repo.Children, name) == nil {
				t.Errorf("repository.%s missing (fragment should have been inlined)", name)
			}
		}
	})

	t.Run("PullRequestStatus_root_keys", func(t *testing.T) {
		q, ok := byOp["PullRequestStatus"]
		if !ok {
			t.Skip("PullRequestStatus not in corpus")
		}
		op, err := parseGraphQLOperation(q)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		wantKeys := []string{"repository", "viewerCreated", "reviewRequested"}
		wantNames := []string{"repository", "search", "search"}
		if len(op.Root) != len(wantKeys) {
			t.Fatalf("expected %d root fields got %d", len(wantKeys), len(op.Root))
		}
		for i, f := range op.Root {
			if f.ResponseKey() != wantKeys[i] {
				t.Errorf("root[%d] ResponseKey = %q want %q", i, f.ResponseKey(), wantKeys[i])
			}
			if f.Name != wantNames[i] {
				t.Errorf("root[%d] Name = %q want %q", i, f.Name, wantNames[i])
			}
		}
	})

	t.Run("PullRequestCreate_mutation_variable_input", func(t *testing.T) {
		q, ok := byOp["PullRequestCreate"]
		if !ok {
			t.Skip("PullRequestCreate not in corpus")
		}
		op, err := parseGraphQLOperation(q)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if op.Kind != "mutation" {
			t.Fatalf("expected Kind=mutation got %q", op.Kind)
		}
		if len(op.Root) != 1 || op.Root[0].Name != "createPullRequest" {
			t.Fatalf("expected root=[createPullRequest] got %v", op.Root)
		}
		args := op.Root[0].Args
		if len(args) != 1 || args[0].Name != "input" {
			t.Fatalf("expected arg input got %v", args)
		}
		if args[0].Value.Kind != gqlVariable || args[0].Value.Raw != "input" {
			t.Fatalf("expected variable $input got %+v", args[0].Value)
		}
	})

	t.Run("Introspection_alias_SearchType", func(t *testing.T) {
		q, ok := byOp["SearchType_enumValues"]
		if !ok {
			t.Skip("SearchType_enumValues not in corpus")
		}
		op, err := parseGraphQLOperation(q)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(op.Root) != 1 {
			t.Fatalf("expected 1 root field got %d", len(op.Root))
		}
		f := op.Root[0]
		if f.Name != "__type" {
			t.Errorf("Name = %q want __type", f.Name)
		}
		if f.Alias != "SearchType" {
			t.Errorf("Alias = %q want SearchType", f.Alias)
		}
		if len(f.Args) != 1 || f.Args[0].Name != "name" {
			t.Fatalf("expected arg name got %v", f.Args)
		}
		if f.Args[0].Value.Kind != gqlString || f.Args[0].Value.Raw != "SearchType" {
			t.Fatalf("expected string SearchType got %+v", f.Args[0].Value)
		}
	})
}

func TestParseGraphQLErrors(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"two_operations", "query A { id } query B { name }"},
		{"subscription", "subscription S { onUpdate { id } }"},
		{"directive_include", "query { id @include(if: true) }"},
		{"block_string", `query { id(a: """hello""") }`},
		{"undefined_fragment", "query { ...Missing }"},
		{"unused_fragment", "fragment Foo on T { id } query { name }"},
		{"fragment_cycle", "fragment A on T { ...B } fragment B on T { ...A } query { ...A }"},
		{"undeclared_variable", "query { repository(owner: $owner) { id } }"},
		{"conflicting_alias", "query { id: name id }"},
		{"same_key_diff_args", `query { a: repository(owner:"x",name:"y"){id} a: repository(owner:"z",name:"y"){id} }`},
		{"depth_30", buildDeepQuery(30)},
		{"oversized", buildLargeQuery(33000)},
		{"unterminated_string", `query { id(a: "hello) }`},
		{"bad_escape", `query { id(a: "\q") }`},
		{"type_definition", "type Foo { a: Int }"},
		{"duplicate_arg", `query { f(a: 1, a: 2) { id } }`},
		{"duplicate_object_field", `query { f(a: {x: 1, x: 2}) { id } }`},
		{"empty_document", ""},
		{"garbage_bytes", "\x00\x01\x02"},
		{"fragment_named_on", "fragment on on T { id } query { ...on }"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseGraphQLOperation(tc.query)
			if err == nil {
				t.Fatalf("expected error for %q", tc.name)
			}
		})
	}
}

// TestParseGraphQLConflictDetection verifies that response-key conflicts nested
// inside a single (non-duplicated) field's children — including those introduced
// by inlining inline fragments and named fragment spreads — are detected.
func TestParseGraphQLConflictDetection(t *testing.T) {
	errCases := []struct {
		name  string
		query string
	}{
		{
			"inline_frag_id_alias_conflict",
			`{repository(owner:"IniZio",name:"nexus"){id ... on Repository{id: name}}}`,
		},
		{
			"nested_sibling_conflict",
			// inside owner{…}: "login" and "login: id" share response key "login" but differ in field name
			`{repository(owner:"a",name:"b"){owner{login login: id}}}`,
		},
		{
			"named_frag_alias_conflict",
			// direct "id" + spread contributes "id: databaseId" → same response key, different name
			`query{repository(owner:"a",name:"b"){id ...f}} fragment f on Repository{id: databaseId}`,
		},
		{
			"deep_nested_conflict",
			// conflict is three levels deep: author{login … on User{login: name}}
			`{repository(owner:"a",name:"b"){pullRequest(number:1){author{login ... on User{login: name}}}}}`,
		},
		{
			"nested_same_key_diff_name",
			// inside repository: "issues" (field issues) vs "issues: pullRequests" (alias issues, field pullRequests)
			`{repository(owner:"a",name:"b"){issues(first:1){nodes{id}} issues: pullRequests(first:1){nodes{id}}}}`,
		},
		{
			"nested_same_key_diff_args",
			`{repository(owner:"a",name:"b"){issues(first:1){nodes{id}} issues(first:2){nodes{id}}}}`,
		},
	}
	for _, tc := range errCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseGraphQLOperation(tc.query)
			if err == nil {
				t.Fatalf("expected conflict error for case %q but got nil", tc.name)
			}
		})
	}

	okCases := []struct {
		name   string
		query  string
		wantID int
	}{
		{
			"legit_duplicate_id",
			`{repository(owner:"a",name:"b"){id id ...on Repository{id}}}`,
			1,
		},
	}
	for _, tc := range okCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			op, err := parseGraphQLOperation(tc.query)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.name, err)
			}
			repo := op.Root[0]
			count := 0
			for _, f := range repo.Children {
				if f.ResponseKey() == "id" {
					count++
				}
			}
			if count != tc.wantID {
				t.Fatalf("expected %d 'id' child(ren) after merge, got %d", tc.wantID, count)
			}
		})
	}
}

func buildDeepQuery(depth int) string {
	open := "query {"
	close := "}"
	for i := 0; i < depth; i++ {
		open += " f {"
		close += " }"
	}
	return open + " id " + close
}

func buildLargeQuery(size int) string {
	q := make([]byte, size)
	for i := range q {
		q[i] = 'x'
	}
	return string(q)
}

func FuzzParseGraphQLOperation(f *testing.F) {
	entries := loadCorpus(f)
	for _, e := range entries {
		var body gqlBody
		if err := json.Unmarshal([]byte(e.Body), &body); err == nil {
			f.Add(body.Query)
		}
	}
	f.Add("")
	f.Add("query { id }")
	f.Add(`query { id @include(if: true) }`)
	f.Add(`subscription { onUpdate { id } }`)
	f.Fuzz(func(t *testing.T, s string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic for input %q: %v", s, r)
			}
		}()
		parseGraphQLOperation(s) //nolint:errcheck
	})
}
