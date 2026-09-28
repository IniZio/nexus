package mitm

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
)

// allowedRepo is a repository the sandbox policy permits access to.
type allowedRepo struct {
	Owner, Name string
	Write       bool // true if mutation/POST allowed
}

// allowedReposFromPolicy derives the set of allowed repositories from a HostPolicy.
//
// A GitHubPolicy contributes a single writable repo.
// Each GlobPattern whose segments are exactly ["","repos",O,R,"**"] — with O and R
// literal (not "*" or "**") — contributes repo O/R. The repo is writable if the
// pattern method is "" (any) or "POST", read-only if "GET", and ignored for any
// other method.  Duplicates are merged with Write=OR. Nil/empty policy returns nil.
func allowedReposFromPolicy(pol HostPolicy) []allowedRepo {
	type entry struct {
		owner, name string
		write       bool
	}
	// ordered map: lowercase "owner/name" → entry (preserves canonical case from first insert)
	order := make([]string, 0)
	merge := make(map[string]*entry)

	add := func(owner, name string, write bool) {
		k := strings.ToLower(owner) + "/" + strings.ToLower(name)
		if e, ok := merge[k]; ok {
			if write {
				e.write = true
			}
		} else {
			merge[k] = &entry{owner: owner, name: name, write: write}
			order = append(order, k)
		}
	}

	if pol.GitHub != nil {
		add(pol.GitHub.Owner, pol.GitHub.Name, true)
	}
	for _, gp := range pol.Patterns {
		// segs must be exactly ["", "repos", O, R, "**"]
		segs := gp.segs
		if len(segs) != 5 {
			continue
		}
		if segs[0] != "" || segs[1] != "repos" || segs[4] != "**" {
			continue
		}
		o, r := segs[2], segs[3]
		if o == "*" || o == "**" || r == "*" || r == "**" {
			continue
		}
		switch strings.ToUpper(gp.method) {
		case "", "POST":
			add(o, r, true)
		case "GET":
			add(o, r, false)
			// any other method: ignored
		}
	}

	if len(order) == 0 {
		return nil
	}
	out := make([]allowedRepo, 0, len(order))
	for _, k := range order {
		e := merge[k]
		out = append(out, allowedRepo{Owner: e.owner, Name: e.name, Write: e.write})
	}
	return out
}

// repoIDCache is a bounded, mutex-protected FIFO cache mapping GitHub node IDs
// (e.g. "R_kgDOAAAAAQ") to canonical "owner/name" strings. A sandbox-scoped
// cache prevents the guest from learning or manipulating node IDs it has not
// been granted.
type repoIDCache struct {
	mu    sync.Mutex
	max   int
	m     map[string]string // nodeID → "owner/name"
	order []string          // insertion order (FIFO eviction)
}

func newRepoIDCache(max int) *repoIDCache {
	return &repoIDCache{max: max, m: make(map[string]string)}
}

// add inserts or updates a nodeID → owner/name mapping. Empty strings are ignored.
// When the cache is full and a new entry would be added, the oldest is evicted.
func (c *repoIDCache) add(nodeID, owner, name string) {
	if nodeID == "" || owner == "" || name == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[nodeID]; exists {
		c.m[nodeID] = owner + "/" + name
		return
	}
	if len(c.m) >= c.max && c.max > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.m, oldest)
	}
	c.m[nodeID] = owner + "/" + name
	c.order = append(c.order, nodeID)
}

// lookup retrieves the owner and name for a node ID.
func (c *repoIDCache) lookup(nodeID string) (owner, name string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	val, exists := c.m[nodeID]
	if !exists {
		return "", "", false
	}
	idx := strings.Index(val, "/")
	if idx < 0 {
		return "", "", false
	}
	return val[:idx], val[idx+1:], true
}

// gqlHarvest records which response fields to inspect to populate the repoIDCache
// from a repository query response. data[RootKey][IDKey] is the node ID of
// the repo identified by Owner/Name.
type gqlHarvest struct {
	RootKey, IDKey, Owner, Name string
}

// graphqlDecision is the outcome of evaluateGitHubGraphQL.
type graphqlDecision struct {
	Allowed bool
	Reason  string
	Harvest []gqlHarvest
}

// evaluateGitHubGraphQL decides whether a POST body to api.github.com/graphql is
// allowed under the sandbox's repo policy. It fails closed: any condition that
// cannot be verified results in a deny.
//
// Reason is a short, non-secret string (never contains variable values that
// could be tokens or secrets). Harvest lists the response-key pairs that the
// caller should read from the 200 response body to warm the repoIDCache.
func evaluateGitHubGraphQL(body []byte, repos []allowedRepo, cache *repoIDCache) graphqlDecision {
	deny := func(reason string) graphqlDecision { return graphqlDecision{Reason: reason} }

	// Rule 1: size limit (1 MiB).
	if len(body) > 1<<20 {
		return deny("body too large")
	}

	// Rule 1: batch detection — a top-level JSON array is a batch request.
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] == '[' {
		return deny("batch")
	}

	if jsonHasDuplicateKeys(body) {
		return deny("duplicate JSON key")
	}

	// Parse the envelope as a raw key → value map for strict key checking.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return deny("invalid JSON")
	}

	// Rule 1: only {query, variables, operationName} are allowed.
	for k := range raw {
		switch k {
		case "query", "variables", "operationName":
		default:
			return deny("unexpected envelope key")
		}
	}

	// Rule 1: query is required and must be a non-empty string.
	queryRaw, hasQuery := raw["query"]
	if !hasQuery {
		return deny("missing query")
	}
	var query string
	if err := json.Unmarshal(queryRaw, &query); err != nil || query == "" {
		return deny("invalid query")
	}

	// Rule 1: variables must be absent, null, or an object.
	vars := make(map[string]json.RawMessage)
	if varRaw, ok := raw["variables"]; ok && string(varRaw) != "null" {
		if err := json.Unmarshal(varRaw, &vars); err != nil {
			return deny("invalid variables")
		}
	}

	// Rule 1: operationName must be absent, null, or a string.
	var opName string
	if onRaw, ok := raw["operationName"]; ok && string(onRaw) != "null" {
		if err := json.Unmarshal(onRaw, &opName); err != nil {
			return deny("invalid operationName")
		}
	}

	// Rule 2: parse the operation with the in-package strict parser.
	op, err := parseGraphQLOperation(query)
	if err != nil {
		return deny("parse error")
	}

	// Rule 1: non-empty operationName must equal the operation name.
	if opName != "" && opName != op.Name {
		return deny("operationName mismatch")
	}

	// Rules 4–9: dispatch by operation kind and root field name.
	var harvest []gqlHarvest

	switch op.Kind {
	case "query":
		for _, f := range op.Root {
			switch f.Name {
			case "__typename":
				// Rule 4: __typename is allowed with no children.
				if len(f.Children) != 0 {
					return deny("__typename must have no children")
				}
			case "repository":
				d := gqlCheckRepository(f, op.VarDefs, vars, repos, cache)
				if !d.Allowed {
					return d
				}
				harvest = append(harvest, d.Harvest...)
			case "search":
				if d := gqlCheckSearch(f, op.VarDefs, vars, repos); !d.Allowed {
					return d
				}
			case "viewer":
				if d := gqlCheckViewer(f); !d.Allowed {
					return d
				}
			case "__type":
				if d := gqlCheckType(f, op.VarDefs, vars); !d.Allowed {
					return d
				}
			default:
				return deny("disallowed root field: " + f.Name)
			}
		}

	case "mutation":
		// Rule 4: mutations must have exactly one root field: createPullRequest.
		if len(op.Root) != 1 {
			return deny("mutation must have exactly one root field")
		}
		f := op.Root[0]
		if f.Name != "createPullRequest" {
			return deny("disallowed mutation: " + f.Name)
		}
		if d := gqlCheckCreatePR(f, op.VarDefs, vars, repos, cache); !d.Allowed {
			return d
		}

	default:
		return deny("disallowed operation kind")
	}

	return graphqlDecision{Allowed: true, Harvest: harvest}
}

// ─── root field checkers ────────────────────────────────────────────────────

// gqlCheckRepository enforces rule 5: repository(owner, name) pinned to allowed repos.
func gqlCheckRepository(f *gqlField, varDefs []gqlVarDef, vars map[string]json.RawMessage, repos []allowedRepo, cache *repoIDCache) graphqlDecision {
	deny := func(r string) graphqlDecision { return graphqlDecision{Reason: r} }

	// Args must be exactly {owner, name}.
	if !gqlArgsExact(f.Args, "owner", "name") {
		return deny("repository: args must be exactly {owner, name}")
	}

	ownerVal, _ := gqlArgByName(f.Args, "owner")
	nameVal, _ := gqlArgByName(f.Args, "name")

	owner, ownerOK := gqlResolveString(ownerVal, varDefs, vars)
	repoName, nameOK := gqlResolveString(nameVal, varDefs, vars)
	if !ownerOK || !nameOK {
		return deny("repository: owner and name must be strings")
	}

	if !validGitHubName(owner) || !validGitHubName(repoName) {
		return deny("repository: owner or name contains invalid characters")
	}

	// (owner, name) must case-insensitively match an allowed repo.
	var matched *allowedRepo
	for i := range repos {
		if asciiEqualFold(repos[i].Owner, owner) && asciiEqualFold(repos[i].Name, repoName) {
			matched = &repos[i]
			break
		}
	}
	if matched == nil {
		return deny("repository: not in allowed repos")
	}

	// Shape allowlist.
	if ok, reason := shapeAllows(githubRootShapes["repository"], f.Children); !ok {
		return deny("repository: shape: " + reason)
	}

	// Rule 5: if a direct child has Name=="id", record a harvest entry.
	var harvest []gqlHarvest
	for _, child := range f.Children {
		if child.Name == "id" {
			harvest = append(harvest, gqlHarvest{
				RootKey: f.ResponseKey(),
				IDKey:   child.ResponseKey(),
				Owner:   matched.Owner,
				Name:    matched.Name,
			})
			break
		}
	}

	return graphqlDecision{Allowed: true, Harvest: harvest}
}

// searchAllowedQualifiers is the whitelist of qualifier keys permitted in
// search(query:...) strings (rule 6).
var searchAllowedQualifiers = map[string]bool{
	"repo": true, "is": true, "state": true, "type": true,
	"author": true, "assignee": true, "label": true, "base": true,
	"head": true, "review-requested": true, "reviewed-by": true,
	"review": true, "draft": true, "sort": true, "mentions": true,
	"milestone": true, "no": true, "archived": true, "in": true,
	"involves": true, "commenter": true, "created": true, "updated": true,
	"merged": true, "closed": true, "status": true, "team-review-requested": true,
}

// gqlCheckSearch enforces rule 6: search(query, type, ...) pinned to allowed repos.
func gqlCheckSearch(f *gqlField, varDefs []gqlVarDef, vars map[string]json.RawMessage, repos []allowedRepo) graphqlDecision {
	deny := func(r string) graphqlDecision { return graphqlDecision{Reason: r} }

	// Args must be a subset of {query, type, first, last, after, before}.
	allowed := map[string]bool{"query": true, "type": true, "first": true, "last": true, "after": true, "before": true}
	for _, a := range f.Args {
		if !allowed[a.Name] {
			return deny("search: unexpected arg: " + a.Name)
		}
	}

	// type is required and must resolve to "ISSUE".
	typeVal, typeOK := gqlArgByName(f.Args, "type")
	if !typeOK {
		return deny("search: missing type arg")
	}
	typeStr, typeResolved := gqlResolveStringLike(typeVal, varDefs, vars)
	if !typeResolved || typeStr != "ISSUE" {
		return deny("search: type must be ISSUE")
	}

	// query: string literal or variable resolving to string.
	var q string
	if queryVal, queryOK := gqlArgByName(f.Args, "query"); queryOK {
		var qOK bool
		q, qOK = gqlResolveString(queryVal, varDefs, vars)
		if !qOK {
			return deny("search: query must be a string")
		}
	}

	// Validate the search query string.
	if reason := validateSearchQuery(q, repos); reason != "" {
		return deny("search: " + reason)
	}

	// Shape allowlist.
	if ok, reason := shapeAllows(githubRootShapes["search"], f.Children); !ok {
		return deny("search: shape: " + reason)
	}

	return graphqlDecision{Allowed: true}
}

// asciiEqualFold compares two strings case-insensitively using ASCII rules only.
// Unlike strings.EqualFold it does not apply Unicode case folding (e.g. U+212A
// KELVIN SIGN would not match 'k').
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca == cb {
			continue
		}
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// validGitHubName returns true iff every byte in s is in [A-Za-z0-9._-].
// It operates on bytes so any multi-byte (non-ASCII) rune fails immediately.
func validGitHubName(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// validateSearchQuery enforces rule 6's query string constraints.
// Returns a non-empty reason string on rejection.
func validateSearchQuery(q string, repos []allowedRepo) string {
	// Whitelist the entire query to ASCII [A-Za-z0-9._@/:-] plus space (0x20).
	// This must run before tokenising so separators like '+', ',' or tabs that
	// GitHub's search engine honours cannot smuggle extra qualifiers.
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == ' ' {
			continue
		}
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == '@' || c == '/' || c == ':' || c == '-') {
			return "query contains invalid character"
		}
	}

	// Split on single ASCII space only; drop empty tokens.
	var tokens []string
	for _, tok := range strings.Split(q, " ") {
		if tok != "" {
			tokens = append(tokens, tok)
		}
	}

	var repoValues []string

	for _, tok := range tokens {
		upper := strings.ToUpper(tok)
		if upper == "AND" || upper == "OR" || upper == "NOT" {
			return "query contains boolean operator"
		}

		// Reject any token with more than one ':'.
		if strings.Count(tok, ":") > 1 {
			return "query token contains multiple colons"
		}

		// Negated terms: -key:value or -text
		if strings.HasPrefix(tok, "-") {
			rest := tok[1:]
			if rest == "" || strings.HasPrefix(rest, "-") {
				return "query contains invalid negated term"
			}
			if colonIdx := strings.Index(rest, ":"); colonIdx >= 0 {
				key := strings.ToLower(rest[:colonIdx])
				if !searchAllowedQualifiers[key] {
					return "query contains negated unknown qualifier: " + key
				}
				if key == "repo" {
					return "query contains negated scope qualifier"
				}
			} else {
				if !isAllowedFreeText(rest) {
					return "query contains invalid negated free-text term"
				}
			}
			continue
		}

		// Qualifier: key:value
		if colonIdx := strings.Index(tok, ":"); colonIdx >= 0 {
			key := strings.ToLower(tok[:colonIdx])
			value := tok[colonIdx+1:]

			if key == "org" || key == "user" || key == "owner" {
				return "query contains org/user/owner qualifier"
			}
			if !searchAllowedQualifiers[key] {
				return "query contains disallowed qualifier: " + key
			}
			if key == "repo" {
				if strings.Count(value, "/") != 1 {
					return "repo qualifier value must be owner/name"
				}
				repoValues = append(repoValues, value)
			}
			continue
		}

		// Free-text token: only [A-Za-z0-9._@/-] allowed.
		if !isAllowedFreeText(tok) {
			return "query contains invalid free-text token"
		}
	}

	if len(repoValues) != 1 {
		return "query must have exactly one repo: qualifier"
	}
	for _, r := range repos {
		if asciiEqualFold(repoValues[0], r.Owner+"/"+r.Name) {
			return ""
		}
	}
	return "repo qualifier does not match any allowed repo"
}

func isAllowedFreeText(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, ch := range s {
		if !((ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') ||
			(ch >= '0' && ch <= '9') ||
			ch == '.' || ch == '_' || ch == '@' || ch == '/' || ch == '-') {
			return false
		}
	}
	return true
}

// gqlCheckViewer enforces rule 8: viewer{login} only, no args.
func gqlCheckViewer(f *gqlField) graphqlDecision {
	if len(f.Args) != 0 {
		return graphqlDecision{Reason: "viewer: unexpected args"}
	}
	if ok, reason := shapeAllows(githubRootShapes["viewer"], f.Children); !ok {
		return graphqlDecision{Reason: "viewer: shape: " + reason}
	}
	return graphqlDecision{Allowed: true}
}

// allowedTypeNames is the set of __type name literals permitted by rule 7.
var allowedTypeNames = map[string]bool{
	"SearchType":                         true,
	"PullRequest":                        true,
	"StatusCheckRollupContextConnection": true,
	"WorkflowRun":                        true,
}

// gqlCheckType enforces rule 7: __type(name: "<literal>") for a fixed set of types.
func gqlCheckType(f *gqlField, varDefs []gqlVarDef, vars map[string]json.RawMessage) graphqlDecision {
	deny := func(r string) graphqlDecision { return graphqlDecision{Reason: r} }

	if !gqlArgsExact(f.Args, "name") {
		return deny("__type: args must be exactly {name}")
	}
	nameVal, _ := gqlArgByName(f.Args, "name")
	if nameVal.Kind != gqlString {
		return deny("__type: name must be a string literal")
	}
	if !allowedTypeNames[nameVal.Raw] {
		return deny("__type: name not in allowed set")
	}

	if ok, reason := shapeAllows(githubRootShapes["__type"], f.Children); !ok {
		return deny("__type: shape: " + reason)
	}

	// Rule 7: nested fields/enumValues args may only be includeDeprecated.
	if reason := checkNestedTypeFieldArgs(f.Children); reason != "" {
		return deny("__type: " + reason)
	}

	return graphqlDecision{Allowed: true}
}

// checkNestedTypeFieldArgs recursively verifies that any `fields` or `enumValues`
// child has only the `includeDeprecated` argument.
func checkNestedTypeFieldArgs(children []*gqlField) string {
	for _, f := range children {
		if f.Name == "fields" || f.Name == "enumValues" {
			for _, a := range f.Args {
				if a.Name != "includeDeprecated" {
					return "nested " + f.Name + " has unexpected arg: " + a.Name
				}
			}
		}
		if reason := checkNestedTypeFieldArgs(f.Children); reason != "" {
			return reason
		}
	}
	return ""
}

// allowedPRInputKeys is the set of keys allowed inside the createPullRequest input
// object (rule 9).
var allowedPRInputKeys = map[string]bool{
	"repositoryId":        true,
	"baseRefName":         true,
	"headRefName":         true,
	"headRepositoryId":    true,
	"title":               true,
	"body":                true,
	"draft":               true,
	"maintainerCanModify": true,
	"clientMutationId":    true,
}

// gqlCheckCreatePR enforces rule 9: createPullRequest(input) mutation.
func gqlCheckCreatePR(f *gqlField, varDefs []gqlVarDef, vars map[string]json.RawMessage, repos []allowedRepo, cache *repoIDCache) graphqlDecision {
	deny := func(r string) graphqlDecision { return graphqlDecision{Reason: r} }

	// At least one writable repo is required.
	hasWrite := false
	for _, r := range repos {
		if r.Write {
			hasWrite = true
			break
		}
	}
	if !hasWrite {
		return deny("createPullRequest: no writable repo in policy")
	}

	// Shape allowlist.
	if ok, reason := shapeAllows(githubRootShapes["createPullRequest"], f.Children); !ok {
		return deny("createPullRequest: shape: " + reason)
	}

	// Args must be exactly {input}.
	if !gqlArgsExact(f.Args, "input") {
		return deny("createPullRequest: args must be exactly {input}")
	}

	inputVal, _ := gqlArgByName(f.Args, "input")

	// Parsed input fields.
	var (
		repositoryId     string
		headRepositoryId string
		headRefName      string
		hasHeadRepoId    bool
		hasHeadRefName   bool
	)

	switch inputVal.Kind {
	case gqlVariable:
		// Resolve variable to a JSON object.
		raw, ok := vars[inputVal.Raw]
		if !ok {
			// Fall back to VarDef default.
			var found bool
			for _, vd := range varDefs {
				if vd.Name == inputVal.Raw && vd.Default != nil && vd.Default.Kind == gqlObject {
					found = true
					break
				}
			}
			if !found {
				return deny("createPullRequest: input variable not in variables")
			}
			return deny("createPullRequest: input variable not in variables")
		}
		if string(raw) == "null" {
			return deny("createPullRequest: input is null")
		}
		var inputMap map[string]json.RawMessage
		if err := json.Unmarshal(raw, &inputMap); err != nil {
			return deny("createPullRequest: input is not an object")
		}
		for k := range inputMap {
			if !allowedPRInputKeys[k] {
				return deny("createPullRequest: unknown input key")
			}
		}
		repoIDRaw, ok := inputMap["repositoryId"]
		if !ok {
			return deny("createPullRequest: repositoryId required")
		}
		if err := json.Unmarshal(repoIDRaw, &repositoryId); err != nil {
			return deny("createPullRequest: repositoryId must be string")
		}
		if hrRaw, ok := inputMap["headRepositoryId"]; ok {
			hasHeadRepoId = true
			if err := json.Unmarshal(hrRaw, &headRepositoryId); err != nil {
				return deny("createPullRequest: headRepositoryId must be string")
			}
		}
		if hrRaw, ok := inputMap["headRefName"]; ok {
			hasHeadRefName = true
			if err := json.Unmarshal(hrRaw, &headRefName); err != nil {
				return deny("createPullRequest: headRefName must be string")
			}
		}

	case gqlObject:
		for _, field := range inputVal.Fields {
			if !allowedPRInputKeys[field.Name] {
				return deny("createPullRequest: unknown input key")
			}
		}
		var found bool
		var ok bool
		repositoryId, found, ok = gqlObjectFieldString(inputVal.Fields, "repositoryId", varDefs, vars)
		if !found {
			return deny("createPullRequest: repositoryId required")
		}
		if !ok {
			return deny("createPullRequest: repositoryId must be string")
		}
		if s, f2, ok2 := gqlObjectFieldString(inputVal.Fields, "headRepositoryId", varDefs, vars); f2 {
			hasHeadRepoId = true
			if !ok2 {
				return deny("createPullRequest: headRepositoryId must be string")
			}
			headRepositoryId = s
		}
		if s, f2, ok2 := gqlObjectFieldString(inputVal.Fields, "headRefName", varDefs, vars); f2 {
			hasHeadRefName = true
			if !ok2 {
				return deny("createPullRequest: headRefName must be string")
			}
			headRefName = s
		}

	default:
		return deny("createPullRequest: input must be a variable or object literal")
	}

	// Rule 9: repositoryId must be in the cache and map to a writable allowed repo.
	if cache == nil {
		return deny("unknown repository node id")
	}
	rOwner, rName, ok := cache.lookup(repositoryId)
	if !ok {
		return deny("unknown repository node id")
	}
	if !repoMatchesWrite(rOwner, rName, repos) {
		return deny("createPullRequest: repositoryId not a writable allowed repo")
	}

	// Rule 9: headRepositoryId (if present) must be in cache and writable.
	if hasHeadRepoId && headRepositoryId != "" {
		hOwner, hName, ok := cache.lookup(headRepositoryId)
		if !ok {
			return deny("unknown repository node id")
		}
		if !repoMatchesWrite(hOwner, hName, repos) {
			return deny("createPullRequest: headRepositoryId not a writable allowed repo")
		}
	}

	// Rule 9: headRefName must not contain ':'.
	if hasHeadRefName && strings.Contains(headRefName, ":") {
		return deny("createPullRequest: headRefName contains ':'")
	}

	return graphqlDecision{Allowed: true}
}

// repoMatchesWrite returns true if owner/name case-insensitively matches a
// writable entry in the repos slice.
func repoMatchesWrite(owner, name string, repos []allowedRepo) bool {
	for _, r := range repos {
		if r.Write && asciiEqualFold(r.Owner, owner) && asciiEqualFold(r.Name, name) {
			return true
		}
	}
	return false
}

// ─── argument helpers ───────────────────────────────────────────────────────

// gqlArgsExact returns true iff the arg list contains exactly the named arguments.
func gqlArgsExact(args []gqlArgument, names ...string) bool {
	if len(args) != len(names) {
		return false
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	for _, a := range args {
		if !set[a.Name] {
			return false
		}
	}
	return true
}

// gqlArgByName finds an argument by exact name.
func gqlArgByName(args []gqlArgument, name string) (gqlValue, bool) {
	for _, a := range args {
		if a.Name == name {
			return a.Value, true
		}
	}
	return gqlValue{}, false
}

// gqlResolveString resolves a gqlValue to a Go string. Handles gqlString
// literals and gqlVariable references that map to a JSON string in vars.
// Falls back to VarDef defaults.
func gqlResolveString(v gqlValue, varDefs []gqlVarDef, vars map[string]json.RawMessage) (string, bool) {
	switch v.Kind {
	case gqlString:
		return v.Raw, true
	case gqlVariable:
		if raw, ok := vars[v.Raw]; ok {
			if string(raw) == "null" {
				return "", false
			}
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return "", false
			}
			return s, true
		}
		// Try VarDef default.
		for _, vd := range varDefs {
			if vd.Name == v.Raw && vd.Default != nil {
				return gqlResolveString(*vd.Default, varDefs, vars)
			}
		}
		return "", false
	}
	return "", false
}

// gqlResolveStringLike resolves a gqlValue to a string representation.
// In addition to gqlString, it also accepts gqlEnum literals (returned as Raw).
func gqlResolveStringLike(v gqlValue, varDefs []gqlVarDef, vars map[string]json.RawMessage) (string, bool) {
	if v.Kind == gqlEnum {
		return v.Raw, true
	}
	return gqlResolveString(v, varDefs, vars)
}

// gqlObjectFieldString extracts a named field from a gqlObject's Fields as a
// string (resolving variables).  Returns (value, found, valid).
func gqlObjectFieldString(fields []gqlObjectField, name string, varDefs []gqlVarDef, vars map[string]json.RawMessage) (val string, found bool, valid bool) {
	for _, f := range fields {
		if f.Name == name {
			s, ok := gqlResolveString(f.Value, varDefs, vars)
			return s, true, ok
		}
	}
	return "", false, false
}

// ─── JSON duplicate-key checker ─────────────────────────────────────────────

// jsonHasDuplicateKeys recursively scans a JSON value for duplicate object keys
// at any depth.  Returns false for non-object JSON or parse errors (the caller's
// Unmarshal will handle those).
func jsonHasDuplicateKeys(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return jsonScanValueDups(dec)
}

func jsonScanValueDups(dec *json.Decoder) bool {
	tok, err := dec.Token()
	if err != nil {
		return false
	}
	d, isDelim := tok.(json.Delim)
	if !isDelim {
		return false // scalar: nothing to check
	}
	if d == '{' {
		seen := make(map[string]bool)
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return false
			}
			key, ok := keyTok.(string)
			if !ok {
				return false
			}
			if seen[key] {
				return true
			}
			seen[key] = true
			if jsonScanValueDups(dec) {
				return true
			}
		}
		dec.Token() // consume '}'
	} else if d == '[' {
		for dec.More() {
			if jsonScanValueDups(dec) {
				return true
			}
		}
		dec.Token()
	}
	return false
}
