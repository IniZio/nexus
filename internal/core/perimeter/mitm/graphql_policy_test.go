package mitm

import (
	"encoding/json"
	"fmt"
	"testing"
)

func nexusRepos(write bool) []allowedRepo {
	return []allowedRepo{{Owner: "IniZio", Name: "nexus", Write: write}}
}

func seededCache() *repoIDCache {
	c := newRepoIDCache(256)
	c.add("R_kgDOAAAAAQ", "IniZio", "nexus")
	return c
}

func TestGraphQLPolicy_CorpusAllowed(t *testing.T) {
	corpus := loadCorpus(t)
	repos := nexusRepos(true)
	cache := seededCache()

	for i, entry := range corpus {
		t.Run(fmt.Sprintf("%d_%s", i, entry.OperationName), func(t *testing.T) {
			d := evaluateGitHubGraphQL([]byte(entry.Body), repos, cache)
			if !d.Allowed {
				t.Errorf("corpus[%d] %s: want allowed, got denied: %s", i, entry.OperationName, d.Reason)
			}
		})
	}
}

func TestGraphQLPolicy_CorpusRepositoryInfoHarvest(t *testing.T) {
	corpus := loadCorpus(t)
	repos := nexusRepos(true)
	cache := seededCache()

	d := evaluateGitHubGraphQL([]byte(corpus[0].Body), repos, cache)
	if !d.Allowed {
		t.Fatalf("RepositoryInfo: denied: %s", d.Reason)
	}
	if len(d.Harvest) == 0 {
		t.Fatal("RepositoryInfo: expected harvest, got none")
	}
	h := d.Harvest[0]
	if h.RootKey != "repository" {
		t.Errorf("Harvest.RootKey = %q, want %q", h.RootKey, "repository")
	}
	if h.IDKey != "id" {
		t.Errorf("Harvest.IDKey = %q, want %q", h.IDKey, "id")
	}
	if h.Owner != "IniZio" {
		t.Errorf("Harvest.Owner = %q, want %q", h.Owner, "IniZio")
	}
	if h.Name != "nexus" {
		t.Errorf("Harvest.Name = %q, want %q", h.Name, "nexus")
	}
}

func TestGraphQLPolicy_CorpusWrongRepo(t *testing.T) {
	corpus := loadCorpus(t)
	repos := []allowedRepo{{Owner: "acme", Name: "other", Write: true}}
	cache := seededCache()

	introspectionOnly := map[int]bool{10: true, 13: true, 14: true}

	for i, entry := range corpus {
		t.Run(fmt.Sprintf("%d_%s", i, entry.OperationName), func(t *testing.T) {
			d := evaluateGitHubGraphQL([]byte(entry.Body), repos, cache)
			if introspectionOnly[i] {
				if !d.Allowed {
					t.Errorf("corpus[%d] %s: introspection should be allowed, got denied: %s", i, entry.OperationName, d.Reason)
				}
			} else {
				if d.Allowed {
					t.Errorf("corpus[%d] %s: want denied for acme/other repo policy, got allowed", i, entry.OperationName)
				}
			}
		})
	}
}

func TestGraphQLPolicy_CreatePREdgeCases(t *testing.T) {
	repos := nexusRepos(true)

	mutation := func(repositoryId string) []byte {
		return []byte(fmt.Sprintf(`{"query":"mutation PullRequestCreate($input:CreatePullRequestInput!){createPullRequest(input:$input){pullRequest{id url}}}","variables":{"input":{"repositoryId":%q,"baseRefName":"main","headRefName":"feat","title":"t"}}}`, repositoryId))
	}

	t.Run("empty_cache_denied", func(t *testing.T) {
		emptyCache := newRepoIDCache(256)
		d := evaluateGitHubGraphQL(mutation("R_kgDOAAAAAQ"), repos, emptyCache)
		if d.Allowed {
			t.Error("want denied with empty cache, got allowed")
		}
	})

	t.Run("wrong_repo_in_cache", func(t *testing.T) {
		c := newRepoIDCache(256)
		c.add("R_kgDOAAAAAQ", "evil", "repo")
		d := evaluateGitHubGraphQL(mutation("R_kgDOAAAAAQ"), repos, c)
		if d.Allowed {
			t.Error("want denied when cached repo doesn't match policy, got allowed")
		}
	})

	t.Run("head_repository_id_unknown", func(t *testing.T) {
		c := seededCache()
		body := []byte(`{"query":"mutation PullRequestCreate($input:CreatePullRequestInput!){createPullRequest(input:$input){pullRequest{id}}}","variables":{"input":{"repositoryId":"R_kgDOAAAAAQ","headRepositoryId":"R_unknown","baseRefName":"main","headRefName":"feat","title":"t"}}}`)
		d := evaluateGitHubGraphQL(body, repos, c)
		if d.Allowed {
			t.Error("want denied for unknown headRepositoryId, got allowed")
		}
	})

	t.Run("read_only_repo_denied", func(t *testing.T) {
		readOnly := []allowedRepo{{Owner: "IniZio", Name: "nexus", Write: false}}
		c := seededCache()
		d := evaluateGitHubGraphQL(mutation("R_kgDOAAAAAQ"), readOnly, c)
		if d.Allowed {
			t.Error("want denied for read-only repo, got allowed")
		}
	})

	t.Run("extra_input_key_denied", func(t *testing.T) {
		c := seededCache()
		body := []byte(`{"query":"mutation PullRequestCreate($input:CreatePullRequestInput!){createPullRequest(input:$input){pullRequest{id}}}","variables":{"input":{"repositoryId":"R_kgDOAAAAAQ","baseRefName":"main","headRefName":"feat","title":"t","unknownField":"evil"}}}`)
		d := evaluateGitHubGraphQL(body, repos, c)
		if d.Allowed {
			t.Error("want denied for extra input key, got allowed")
		}
	})

	t.Run("head_ref_name_with_colon_denied", func(t *testing.T) {
		c := seededCache()
		body := []byte(`{"query":"mutation PullRequestCreate($input:CreatePullRequestInput!){createPullRequest(input:$input){pullRequest{id}}}","variables":{"input":{"repositoryId":"R_kgDOAAAAAQ","baseRefName":"main","headRefName":"evil:branch","title":"t"}}}`)
		d := evaluateGitHubGraphQL(body, repos, c)
		if d.Allowed {
			t.Error("want denied for headRefName with colon, got allowed")
		}
	})
}

func TestGraphQLPolicy_Smuggling(t *testing.T) {
	repos := nexusRepos(true)
	cache := seededCache()

	cases := []struct {
		name string
		body string
	}{
		{
			"extra_evil_root_alongside",
			`{"query":"query{repository(owner:\"IniZio\",name:\"nexus\"){id} evil: repository(owner:\"evil\",name:\"x\"){id}}"}`,
		},
		{
			"alias_hiding",
			`{"query":"query{viewer: repository(owner:\"evil\",name:\"x\"){id}}"}`,
		},
		{
			"node_id_root",
			`{"query":"query{node(id:\"R_x\"){...on Repository{name}}}"}`,
		},
		{
			"organization_root",
			`{"query":"query{organization(login:\"x\"){repositories{nodes{name}}}}"}`,
		},
		{
			"mutation_two_roots",
			`{"query":"mutation{createPullRequest(input:{repositoryId:\"R_kgDOAAAAAQ\",baseRefName:\"main\",headRefName:\"feat\",title:\"t\"}){pullRequest{id}} deleteRepository(input:{repositoryId:\"R_x\"}){clientMutationId}}"}`,
		},
		{
			"__schema",
			`{"query":"query{__schema{types{name}}}"}`,
		},
		{
			"__type_repository",
			`{"query":"query{__type(name:\"Repository\"){fields{name}}}"}`,
		},
		{
			"repository_follow_renames_arg",
			`{"query":"query{repository(owner:\"IniZio\",name:\"nexus\",followRenames:true){id}}"}`,
		},
		{
			"batch_array",
			`[{"query":"query{viewer{login}}"},{"query":"query{viewer{login}}"}]`,
		},
		{
			"extensions_key",
			`{"query":"query{viewer{login}}","extensions":{"persistedQuery":{"version":1}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := evaluateGitHubGraphQL([]byte(tc.body), repos, cache)
			if d.Allowed {
				t.Errorf("%s: want denied, got allowed", tc.name)
			}
		})
	}
}

func TestGraphQLPolicy_SmugglingOperationName(t *testing.T) {
	repos := nexusRepos(true)
	cache := seededCache()

	body := []byte(`{"query":"query RepositoryInfo($owner:String!,$name:String!){repository(owner:$owner,name:$name){id}}","variables":{"owner":"IniZio","name":"nexus"},"operationName":"WrongName"}`)
	d := evaluateGitHubGraphQL(body, repos, cache)
	if d.Allowed {
		t.Error("operationName mismatch: want denied, got allowed")
	}
}

func TestGraphQLPolicy_SmugglingOwnerVarType(t *testing.T) {
	repos := nexusRepos(true)
	cache := seededCache()

	t.Run("owner_is_object", func(t *testing.T) {
		body := []byte(`{"query":"query Q($owner:String!,$name:String!){repository(owner:$owner,name:$name){id}}","variables":{"owner":{"login":"IniZio"},"name":"nexus"}}`)
		d := evaluateGitHubGraphQL(body, repos, cache)
		if d.Allowed {
			t.Error("owner as JSON object: want denied, got allowed")
		}
	})

	t.Run("owner_is_number", func(t *testing.T) {
		body := []byte(`{"query":"query Q($owner:String!,$name:String!){repository(owner:$owner,name:$name){id}}","variables":{"owner":42,"name":"nexus"}}`)
		d := evaluateGitHubGraphQL(body, repos, cache)
		if d.Allowed {
			t.Error("owner as JSON number: want denied, got allowed")
		}
	})
}

func TestGraphQLPolicy_SmugglingDuplicateNestedKey(t *testing.T) {
	repos := nexusRepos(true)
	cache := seededCache()

	body := []byte(`{"query":"mutation PullRequestCreate($input:CreatePullRequestInput!){createPullRequest(input:$input){pullRequest{id}}}","variables":{"input":{"repositoryId":"R_kgDOAAAAAQ","repositoryId":"R_evil","baseRefName":"main","headRefName":"feat","title":"t"}}}`)
	d := evaluateGitHubGraphQL(body, repos, cache)
	if d.Allowed {
		t.Error("duplicate nested JSON key: want denied, got allowed")
	}
}

func TestGraphQLPolicy_Search(t *testing.T) {
	repos := nexusRepos(true)
	cache := seededCache()

	deny := func(t *testing.T, name, body string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			d := evaluateGitHubGraphQL([]byte(body), repos, cache)
			if d.Allowed {
				t.Errorf("%s: want denied, got allowed", name)
			}
		})
	}

	deny(t, "extra_repo_qualifier",
		`{"query":"query PullRequestSearch($q:String!,$type:SearchType!){search(query:$q,type:$type,first:30){issueCount}}","variables":{"q":"repo:IniZio/nexus repo:evil/x state:open","type":"ISSUE"}}`)

	deny(t, "multi_repo_policy_last",
		`{"query":"query PRS($q:String!,$t:SearchType!){search(query:$q,type:$t,first:1){issueCount}}","variables":{"q":"repo:other/x repo:IniZio/nexus state:open","t":"ISSUE"}}`)

	deny(t, "duplicate_repo_qualifier",
		`{"query":"query PRS($q:String!,$t:SearchType!){search(query:$q,type:$t,first:1){issueCount}}","variables":{"q":"repo:IniZio/nexus repo:IniZio/nexus","t":"ISSUE"}}`)

	deny(t, "multi_repo_policy_first",
		`{"query":"query PRS($q:String!,$t:SearchType!){search(query:$q,type:$t,first:1){issueCount}}","variables":{"q":"repo:IniZio/nexus repo:other/x","t":"ISSUE"}}`)

	deny(t, "org_qualifier",
		`{"query":"query PullRequestSearch($q:String!,$type:SearchType!){search(query:$q,type:$type,first:30){issueCount}}","variables":{"q":"org:acme repo:IniZio/nexus state:open","type":"ISSUE"}}`)

	deny(t, "bare_OR",
		`{"query":"query PullRequestSearch($q:String!,$type:SearchType!){search(query:$q,type:$type,first:30){issueCount}}","variables":{"q":"repo:IniZio/nexus OR state:open","type":"ISSUE"}}`)

	deny(t, "negated_repo",
		`{"query":"query PullRequestSearch($q:String!,$type:SearchType!){search(query:$q,type:$type,first:30){issueCount}}","variables":{"q":"repo:IniZio/nexus -repo:IniZio/nexus state:open","type":"ISSUE"}}`)

	deny(t, "quotes_in_query",
		`{"query":"query PullRequestSearch($q:String!,$type:SearchType!){search(query:$q,type:$type,first:30){issueCount}}","variables":{"q":"repo:IniZio/nexus \"evil\"","type":"ISSUE"}}`)

	deny(t, "parens_in_query",
		`{"query":"query PullRequestSearch($q:String!,$type:SearchType!){search(query:$q,type:$type,first:30){issueCount}}","variables":{"q":"repo:IniZio/nexus (state:open)","type":"ISSUE"}}`)

	deny(t, "missing_repo_qualifier",
		`{"query":"query PullRequestSearch($q:String!,$type:SearchType!){search(query:$q,type:$type,first:30){issueCount}}","variables":{"q":"state:open type:pr","type":"ISSUE"}}`)

	deny(t, "type_REPOSITORY",
		`{"query":"query PullRequestSearch($q:String!,$type:SearchType!){search(query:$q,type:$type,first:30){issueCount}}","variables":{"q":"repo:IniZio/nexus state:open","type":"REPOSITORY"}}`)

	t.Run("gh_viewer_query_allowed", func(t *testing.T) {
		body := `{"query":"query PullRequestSearch($q:String!,$type:SearchType!){search(query:$q,type:$type,first:30){issueCount edges{node{...on PullRequest{number title state url headRefName}}}}}","variables":{"q":"author:@me repo:IniZio/nexus state:open type:pr","type":"ISSUE"}}`
		d := evaluateGitHubGraphQL([]byte(body), repos, cache)
		if !d.Allowed {
			t.Errorf("gh viewer search query: want allowed, got denied: %s", d.Reason)
		}
	})

	t.Run("gh_review_requested_allowed", func(t *testing.T) {
		body := `{"query":"query PullRequestSearch($q:String!,$type:SearchType!){search(query:$q,type:$type,first:30){issueCount edges{node{...on PullRequest{number title}}}}}","variables":{"q":"repo:IniZio/nexus state:open review-requested:@me","type":"ISSUE"}}`
		d := evaluateGitHubGraphQL([]byte(body), repos, cache)
		if !d.Allowed {
			t.Errorf("gh review-requested search query: want allowed, got denied: %s", d.Reason)
		}
	})
}

// searchBody builds a minimal valid search request body with the given raw query string.
// The query string is JSON-marshaled so control chars and special chars are escaped correctly.
func searchBody(q string) []byte {
	b, _ := json.Marshal(q)
	return []byte(`{"query":"query PRS($q:String!,$t:SearchType!){search(query:$q,type:$t,first:1){issueCount}}","variables":{"q":` + string(b) + `,"t":"ISSUE"}}`)
}

func TestGraphQLPolicy_SearchCharsetAndStructure(t *testing.T) {
	repos := nexusRepos(true)
	cache := seededCache()

	deny := func(t *testing.T, name, q string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			d := evaluateGitHubGraphQL(searchBody(q), repos, cache)
			if d.Allowed {
				t.Errorf("%s: want denied, got allowed", name)
			}
		})
	}
	allow := func(t *testing.T, name, q string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			d := evaluateGitHubGraphQL(searchBody(q), repos, cache)
			if !d.Allowed {
				t.Errorf("%s: want allowed, got denied: %s", name, d.Reason)
			}
		})
	}

	deny(t, "plus_separator_smuggle", "repo:IniZio/nexus state:open+repo:evil/x")

	// ',' similarly
	deny(t, "comma_separator_smuggle", "repo:IniZio/nexus state:open,repo:evil/x")

	// tab: strings.Fields splits on it but our split-on-space-only must not
	deny(t, "tab_in_query", "repo:IniZio/nexus\tstate:open")

	// non-ASCII rune
	deny(t, "non_ascii_rune", "repo:IniZio/nexus é")

	// token with more than one ':' (could hide second qualifier key after value)
	deny(t, "multiple_colons_token", "repo:IniZio/nexus label:a:b")

	deny(t, "repo_value_two_slashes", "repo:IniZio/nexus/x")

	allow(t, "author_me_query", "author:@me repo:IniZio/nexus state:open type:pr")
	allow(t, "review_requested_query", "repo:IniZio/nexus state:open review-requested:@me")
	allow(t, "is_pr_query", "repo:IniZio/nexus state:open is:pr author:@me")
}

func TestGraphQLPolicy_RepositoryASCIIValidation(t *testing.T) {
	t.Run("kelvin_sign_owner_denied", func(t *testing.T) {
		repos := []allowedRepo{{Owner: "kacme", Name: "x", Write: true}}
		cache := newRepoIDCache(256)
		body := []byte(`{"query":"query Q($o:String!,$n:String!){repository(owner:$o,name:$n){id}}","variables":{"o":"` + "KKcme" + `","n":"x"}}`)
		d := evaluateGitHubGraphQL(body, repos, cache)
		if d.Allowed {
			t.Error("Kelvin-sign owner (U+212A): want denied, got allowed")
		}
	})

	t.Run("ascii_case_fold_allowed", func(t *testing.T) {
		repos := []allowedRepo{{Owner: "kacme", Name: "x", Write: true}}
		cache := newRepoIDCache(256)
		body := []byte(`{"query":"query Q($o:String!,$n:String!){repository(owner:$o,name:$n){id}}","variables":{"o":"Kacme","n":"x"}}`)
		d := evaluateGitHubGraphQL(body, repos, cache)
		if !d.Allowed {
			t.Errorf("ASCII case fold: want allowed, got denied: %s", d.Reason)
		}
	})

	t.Run("search_wrong_repo_case_denied", func(t *testing.T) {
		repos := nexusRepos(true)
		cache := seededCache()
		d := evaluateGitHubGraphQL(searchBody("repo:Kacme/x state:open"), repos, cache)
		if d.Allowed {
			t.Error("search repo:Kacme/x: want denied (not in allowed repos), got allowed")
		}
	})
}

func TestGraphQLPolicy_AllowedReposFromPolicy(t *testing.T) {
	gh := HostPolicy{GitHub: &GitHubPolicy{Owner: "IniZio", Name: "nexus"}}
	repos := allowedReposFromPolicy(gh)
	if len(repos) != 1 {
		t.Fatalf("GitHub policy: want 1 repo, got %d", len(repos))
	}
	if repos[0].Owner != "IniZio" || repos[0].Name != "nexus" || !repos[0].Write {
		t.Errorf("GitHub policy repo: got %+v", repos[0])
	}

	t.Run("nil_policy", func(t *testing.T) {
		repos := allowedReposFromPolicy(HostPolicy{})
		if len(repos) != 0 {
			t.Errorf("nil policy: want nil, got %v", repos)
		}
	})

	t.Run("glob_patterns", func(t *testing.T) {
		p1, err := CompileGlobPattern("/repos/IniZio/nexus/**")
		if err != nil {
			t.Fatal(err)
		}
		p2, err := CompileGlobPattern("GET /repos/a/b/**")
		if err != nil {
			t.Fatal(err)
		}
		p3, err := CompileGlobPattern("/repos/*/x/**")
		if err != nil {
			t.Fatal(err)
		}
		p4, err := CompileGlobPattern("/user")
		if err != nil {
			t.Fatal(err)
		}
		pol := HostPolicy{Patterns: []GlobPattern{p1, p2, p3, p4}}
		repos := allowedReposFromPolicy(pol)

		found := make(map[string]allowedRepo)
		for _, r := range repos {
			found[r.Owner+"/"+r.Name] = r
		}

		r, ok := found["IniZio/nexus"]
		if !ok {
			t.Error("IniZio/nexus: not found")
		} else if !r.Write {
			t.Error("IniZio/nexus: want Write=true")
		}

		r, ok = found["a/b"]
		if !ok {
			t.Error("a/b: not found")
		} else if r.Write {
			t.Error("a/b: want Write=false (GET)")
		}

		if _, ok := found["*/x"]; ok {
			t.Error("wildcard owner pattern should be ignored")
		}

		// /user has only 2 segs: should be ignored
		for k := range found {
			if k == "user" || k == "/user" {
				t.Errorf("unexpected entry for /user pattern: %s", k)
			}
		}

		if len(repos) != 2 {
			t.Errorf("want 2 repos (IniZio/nexus + a/b), got %d: %v", len(repos), repos)
		}
	})

	t.Run("write_merge", func(t *testing.T) {
		p1, _ := CompileGlobPattern("GET /repos/X/Y/**")
		p2, _ := CompileGlobPattern("POST /repos/X/Y/**")
		pol := HostPolicy{Patterns: []GlobPattern{p1, p2}}
		repos := allowedReposFromPolicy(pol)
		if len(repos) != 1 {
			t.Fatalf("want 1 merged repo, got %d", len(repos))
		}
		if !repos[0].Write {
			t.Error("merged X/Y: want Write=true (OR)")
		}
	})
}

func TestGraphQLPolicy_RepoIDCache(t *testing.T) {
	c := newRepoIDCache(2)

	c.add("id1", "o1", "r1")
	c.add("id2", "o2", "r2")

	if o, n, ok := c.lookup("id1"); !ok || o != "o1" || n != "r1" {
		t.Errorf("id1: got (%q,%q,%v)", o, n, ok)
	}
	if o, n, ok := c.lookup("id2"); !ok || o != "o2" || n != "r2" {
		t.Errorf("id2: got (%q,%q,%v)", o, n, ok)
	}

	c.add("id3", "o3", "r3")

	if _, _, ok := c.lookup("id1"); ok {
		t.Error("id1 should have been evicted (FIFO)")
	}
	if o, n, ok := c.lookup("id2"); !ok || o != "o2" || n != "r2" {
		t.Errorf("id2 after eviction: got (%q,%q,%v)", o, n, ok)
	}
	if o, n, ok := c.lookup("id3"); !ok || o != "o3" || n != "r3" {
		t.Errorf("id3: got (%q,%q,%v)", o, n, ok)
	}

	t.Run("ignore_empty", func(t *testing.T) {
		sizeBefore := len(c.m)
		c.add("", "o4", "r4")
		c.add("id4", "", "r4")
		c.add("id5", "o5", "")
		if len(c.m) != sizeBefore {
			t.Errorf("empty fields should be ignored: size %d→%d", sizeBefore, len(c.m))
		}
	})
}

func TestGraphQLPolicy_EnvelopeRules(t *testing.T) {
	repos := nexusRepos(true)
	cache := seededCache()

	t.Run("body_too_large", func(t *testing.T) {
		large := make([]byte, 1<<20+1)
		large[0] = '{'
		d := evaluateGitHubGraphQL(large, repos, cache)
		if d.Allowed {
			t.Error("body too large: want denied")
		}
	})

	t.Run("missing_query", func(t *testing.T) {
		d := evaluateGitHubGraphQL([]byte(`{"variables":{}}`), repos, cache)
		if d.Allowed {
			t.Error("missing query: want denied")
		}
	})

	t.Run("empty_query", func(t *testing.T) {
		d := evaluateGitHubGraphQL([]byte(`{"query":""}`), repos, cache)
		if d.Allowed {
			t.Error("empty query: want denied")
		}
	})

	t.Run("variables_not_object", func(t *testing.T) {
		d := evaluateGitHubGraphQL([]byte(`{"query":"query{viewer{login}}","variables":"string"}`), repos, cache)
		if d.Allowed {
			t.Error("variables as string: want denied")
		}
	})

	t.Run("extra_undeclared_variable_tolerated", func(t *testing.T) {
		body := `{"query":"query PullRequestByNumber($owner:String!,$repo:String!,$pr_number:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$pr_number){number title}}}","variables":{"owner":"IniZio","repo":"nexus","pr_number":7,"number":7}}`
		d := evaluateGitHubGraphQL([]byte(body), repos, cache)
		if !d.Allowed {
			t.Errorf("extra undeclared variable should be tolerated: %s", d.Reason)
		}
	})
}

func TestGraphQLPolicy_SearchNegatedTerms(t *testing.T) {
	repos := nexusRepos(true)
	cache := seededCache()

	deny := func(t *testing.T, name, q string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			d := evaluateGitHubGraphQL(searchBody(q), repos, cache)
			if d.Allowed {
				t.Errorf("%s: want denied, got allowed", name)
			}
		})
	}
	allow := func(t *testing.T, name, q string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			d := evaluateGitHubGraphQL(searchBody(q), repos, cache)
			if !d.Allowed {
				t.Errorf("%s: want allowed, got denied: %s", name, d.Reason)
			}
		})
	}

	deny(t, "double_dash_label", "--label:x repo:IniZio/nexus")
	deny(t, "negated_unknown_key", "-foo:bar repo:IniZio/nexus")
	deny(t, "negated_repo_qualifier", "-repo:IniZio/nexus repo:IniZio/nexus")
	deny(t, "negated_org_qualifier", "-org:x repo:IniZio/nexus")
	deny(t, "bare_dash", "- repo:IniZio/nexus")
	allow(t, "negated_is_draft", "-is:draft repo:IniZio/nexus")
}
