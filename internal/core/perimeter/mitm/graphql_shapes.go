package mitm

// gqlShape describes an allowed selection-set subtree.
// A nil or empty children map means the field is a leaf (scalar/enum).
type gqlShape struct {
	children map[string]*gqlShape
}

// fieldsToShape converts an already-inlined, merged field list to a gqlShape.
// Each field's Name is a key; duplicate Names are last-wins (the shape doc
// must not produce them — see repositoryShapeDoc notes).
func fieldsToShape(fields []*gqlField) *gqlShape {
	if len(fields) == 0 {
		return &gqlShape{}
	}
	m := make(map[string]*gqlShape, len(fields))
	for _, f := range fields {
		m[f.Name] = fieldsToShape(f.Children)
	}
	return &gqlShape{children: m}
}

// mustParseShape parses a shape document (shorthand query + optional fragment
// definitions) and returns the gqlShape for the named root field's children.
// Panics on any error — the inputs are package-level constants.
func mustParseShape(rootName, doc string) *gqlShape {
	op, err := parseGraphQLOperation(doc)
	if err != nil {
		panic("graphql_shapes: " + rootName + ": " + err.Error())
	}
	for _, f := range op.Root {
		if f.Name == rootName {
			return fieldsToShape(f.Children)
		}
	}
	panic("graphql_shapes: root field " + rootName + " not found in shape doc")
}

// githubRootShapes maps each allowed query/mutation root field name to its
// permitted selection-set shape.  Built at package init from corpus-derived
// constants below.  A root field whose Name is not a key in this map is
// denied at the policy layer before shapeAllows is called.
var githubRootShapes map[string]*gqlShape

func init() {
	githubRootShapes = map[string]*gqlShape{
		"repository":        mustParseShape("repository", repositoryShapeDoc),
		"search":            mustParseShape("search", searchShapeDoc),
		"viewer":            mustParseShape("viewer", viewerShapeDoc),
		"__type":            mustParseShape("__type", typeShapeDoc),
		"createPullRequest": mustParseShape("createPullRequest", createPRShapeDoc),
	}
}

// shapeAllows reports whether every field in fields (recursively) is
// permitted by shape.  On allow it returns (true, "").  On deny it returns
// (false, dotted-path) of the first offending field relative to this call
// site (e.g. "owner.repositories" when called with the repository shape and
// repository's children).
//
// Rules applied for each request field f:
//   - If f.Name == "__typename" and f has no children → always allowed.
//   - Otherwise f.Name must be a key in shape.children.
//   - If the shape child is a leaf (nil or empty children map) → f must have
//     no children.
//   - If the shape child is non-leaf → f must have children, and they are
//     checked recursively.
//   - Arguments are not checked.
func shapeAllows(shape *gqlShape, fields []*gqlField) (ok bool, path string) {
	return shapeAllowsInner(shape, fields, "")
}

func shapeAllowsInner(shape *gqlShape, fields []*gqlField, prefix string) (bool, string) {
	for _, f := range fields {
		full := f.Name
		if prefix != "" {
			full = prefix + "." + f.Name
		}
		// __typename is always allowed as a bare leaf.
		if f.Name == "__typename" {
			if len(f.Children) != 0 {
				return false, full
			}
			continue
		}
		child, exists := shape.children[f.Name]
		if !exists {
			return false, full
		}
		isLeaf := child == nil || len(child.children) == 0
		if isLeaf {
			if len(f.Children) != 0 {
				// Leaf in shape but request selects sub-fields.
				return false, full
			}
		} else {
			if len(f.Children) == 0 {
				// Non-leaf in shape but request has no sub-selection.
				return false, full
			}
			if ok2, p := shapeAllowsInner(child, f.Children, full); !ok2 {
				return false, p
			}
		}
	}
	return true, ""
}

// ── shape document constants ───────────────────────────────────────────────
//
// Each constant is a self-contained GraphQL document (optional named
// fragments + an anonymous shorthand query containing exactly one root field).
// The constants are the UNION of field paths observed in the corpus
// (internal/core/perimeter/mitm/testdata/gh_graphql_corpus.json, 17 bodies
// from gh 2.100.0).  Cross-repo pointer fields (parent, headRepository,
// headRepositoryOwner, author, owner) expose only the identity scalars the
// corpus selects — never connection fields (repositories, pullRequests, …).

// repositoryShapeDoc — union of all field paths under "repository".
//
//	RepositoryInfo         (gh pr create)      — id, databaseId, name, owner,
//	                                              flags, parent{same}, merge policies
//	PullRequestForBranch   (gh pr create/view) — pullRequests connection, full PR body
//	PullRequestByNumber    (gh pr view x3,     — pullRequest(number) single PR,
//	                        gh pr checkout)       full + slim + checkout variants
//	PullRequestProjectItems (gh pr view)        — pullRequest.projectItems connection
//	PullRequestList        (gh pr list x3)      — pullRequests connection, slim PR body
//	PullRequestStatus.repository (gh pr status) — pullRequests edges with prWithReviews
const repositoryShapeDoc = `
fragment prFields on PullRequest {
  url id number state title body
  baseRefName headRefName isCrossRepository isDraft
  maintainerCanModify mergeable additions deletions createdAt
  mergeStateStatus reviewDecision

  author            { login id name }
  headRepositoryOwner { id login name }
  headRepository    { id name nameWithOwner }

  baseRef { branchProtectionRule { requiresStrictStatusChecks } }

  autoMergeRequest {
    authorEmail commitBody commitHeadline mergeMethod enabledAt
    enabledBy { login id name }
  }

  commits {
    totalCount
    nodes {
      commit {
        statusCheckRollup {
          contexts {
            checkRunCount
            checkRunCountsByState { state count }
            statusContextCount
            statusContextCountsByState { state count }
            nodes {
              context state targetUrl createdAt description
              name
              checkSuite { workflowRun { workflow { name } } }
              status conclusion startedAt completedAt detailsUrl
            }
            pageInfo { hasNextPage endCursor }
          }
        }
      }
    }
  }

  reviewRequests {
    nodes {
      requestedReviewer {
        login name slug
        organization { login }
      }
    }
  }

  reviews {
    totalCount
    nodes {
      id
      author { login }
      authorAssociation submittedAt body state
      commit { oid }
      reactionGroups { content users { totalCount } }
    }
    pageInfo { hasNextPage endCursor }
  }

  assignees {
    totalCount
    nodes { id login name databaseId }
  }

  labels {
    totalCount
    nodes { id name description color }
  }

  milestone { number title description dueOn }

  comments {
    totalCount
    nodes {
      id
      author { login id name }
      authorAssociation body createdAt includesCreatedEdit
      isMinimized minimizedReason
      reactionGroups { content users { totalCount } }
      url viewerDidAuthor
    }
    pageInfo { hasNextPage endCursor }
  }

  reactionGroups { content users { totalCount } }

  latestReviews {
    nodes {
      author { login }
      authorAssociation submittedAt body state
    }
  }
}

{
  repository {
    id databaseId name
    owner { login }
    hasIssuesEnabled description hasWikiEnabled viewerPermission
    defaultBranchRef { name }
    mergeCommitAllowed rebaseMergeAllowed squashMergeAllowed

    parent {
      id databaseId name
      owner { login }
      hasIssuesEnabled description hasWikiEnabled viewerPermission
      defaultBranchRef { name }
    }

    pullRequests {
      totalCount
      nodes { ...prFields }
      edges { node { ...prFields } }
      pageInfo { hasNextPage endCursor }
    }

    pullRequest {
      ...prFields
      projectItems {
        totalCount
        nodes {
          id
          project { id title }
          fieldValueByName { optionId name }
        }
        pageInfo { hasNextPage endCursor }
      }
    }
  }
}
`

// searchShapeDoc — union of all field paths under "search" (and aliased roots
// viewerCreated/reviewRequested that also have Name="search").
//
//	PullRequestSearch            (gh pr list --author @me) — nodes with slim PR fields
//	PullRequestStatus viewerCreated   (gh pr status)       — edges.node with prWithReviews
//	PullRequestStatus reviewRequested (gh pr status)       — edges.node with slim+status PR
//
// nodes vs edges.node carry different field sets per corpus; they are defined
// separately to avoid adding fields not seen in the corpus for each sub-path.
const searchShapeDoc = `
fragment searchSimplePr on PullRequest {
  number title state url headRefName
  headRepositoryOwner { id login name }
  isCrossRepository isDraft createdAt
}

fragment searchStatusPr on PullRequest {
  number title state url isDraft isCrossRepository headRefName
  headRepositoryOwner { id login name }
  mergeStateStatus reviewDecision
  baseRef { branchProtectionRule { requiresStrictStatusChecks } }
  autoMergeRequest {
    authorEmail commitBody commitHeadline mergeMethod enabledAt
    enabledBy { login id name }
  }
  commits {
    nodes {
      commit {
        statusCheckRollup {
          contexts {
            checkRunCount
            checkRunCountsByState { state count }
            statusContextCount
            statusContextCountsByState { state count }
          }
        }
      }
    }
  }
  latestReviews {
    nodes {
      author { login }
      authorAssociation submittedAt body state
    }
  }
}

{
  search {
    issueCount
    nodes { ...searchSimplePr }
    edges { node { ...searchStatusPr } }
    pageInfo { hasNextPage endCursor }
  }
}
`

// viewerShapeDoc — only {login} is permitted, matching the REST GET /user
// carve-out.  Source: design doc §3 ("viewer: only {login}").
const viewerShapeDoc = `{ viewer { login } }`

// typeShapeDoc — allowed fields under "__type" root.
//
//	SearchType_enumValues  (gh pr list --author @me) — enumValues{name}
//	PullRequest_fields     (gh pr status)             — fields{name} (two aliases)
//	PullRequest_fields2    (gh pr status)             — fields{name}
const typeShapeDoc = `{
  __type {
    enumValues { name }
    fields     { name }
  }
}`

// createPRShapeDoc — allowed fields under the "createPullRequest" mutation root.
//
//	PullRequestCreate (gh pr create, both plain and --draft variants).
const createPRShapeDoc = `{
  createPullRequest {
    pullRequest {
      id
      url
    }
  }
}`
