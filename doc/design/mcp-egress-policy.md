# MCP Egress Policy — design notes

**Branch**: develop  
**Status**: living document; authoritative for the `egress.mcp` config surface and proxy decisions

See `plugins/claude/skills/nexus/references/egress.md §MCP Endpoint Policies` for the user-facing schema and decision table.

---

## Why an allow-list, not a deny-list

A deny-list requires enumerating every tool that must be blocked — including tools that do not exist yet. An allow-list is composable and safe by default: a sandbox can only call tools it was explicitly granted, and a new tool added by the upstream MCP server is denied until added to `allow`. This is the same reasoning behind default-deny path policies and default-deny egress hosts.

## Why HTTP 200 for tool denial

The JSON-RPC spec requires that tool-call responses carry HTTP 200 regardless of whether the call succeeded or was rejected by policy. MCP clients treat a non-200 response as a transport-layer error and may retry the call, surface a confusing "server unavailable" message, or suppress the structured error object entirely. Returning HTTP 200 with a JSON-RPC `-32001` error object ensures the error is delivered through the application protocol where the client (and agent) can inspect and handle it correctly. The request `id` is echoed so the caller can correlate the response.

Parse and decode failures (malformed JSON, duplicate top-level keys, unsupported Content-Encoding) are different: these indicate the sender is not behaving like an MCP client, so fail-closed HTTP 403 with `-32700` (parse error) or `-32600` (invalid request) is appropriate.

## Why request-side is the security boundary

`tools/list` response filtering removes allow-listed tools from the server's advertised tool roster, which improves UX (an agent sees only what it may call). It is not the security boundary: a sufficiently determined caller can invoke any tool directly without consulting `tools/list` first. The `OnRequest` handler — which evaluates every `tools/call` against the allow list before forwarding — is the actual enforcement point. These two roles are intentionally separated.

## Why evaluation before credential swap

Policy enforcement answers the question "is this sandbox allowed to perform this action?" and must be answered before any credential is injected. Evaluating after credential injection creates a confused-deputy: the credential would already be attached to a request that policy subsequently denies, and the service could observe a partially-credentialed request. The MCP policy handler runs in the same position as path-policy and GraphQL handlers, ahead of the credential-swap step.

## Case-sensitive exact-key decoding and duplicate rejection

JSON keys are case-sensitive. `tools/call` and `Tools/Call` are different method names; a policy that allows `tools/call` does not allow `Tools/Call`. Duplicate keys at the top level or inside `params` are rejected (fail-closed) because the JSON spec does not define which value wins and implementations differ — accepting ambiguous input opens a desync between what nexus evaluated and what the upstream server parsed.

## MCP hosts in the secret-host set

The MITM proxy only intercepts hosts listed in `SecretHosts` (or `SecretHostSuffixes`). If an MCP policy host is not in that set, TLS traffic to it is forwarded opaque and policy checks never run. To prevent this silent fail-open, the service layer adds every `egress.mcp` host to `SecretHosts` before constructing the proxy, regardless of whether the host also appears in `egress.secrets`. The proxy then presents a forged certificate for that host, decrypts the request, evaluates policy, and (if allowed) re-encrypts before forwarding.

## Ordering relative to other handlers

Request handlers are registered in this order: path-policy → GraphQL → MCP → credential swap. MCP sits after path-policy so that a host covered by both a path policy and an MCP policy gets path-filtered first (the coarser guard), then tool-filtered (the finer guard). It sits before credential swap so policy is evaluated on the sandbox's identity, not the injected credential's identity.

## Key-fold rejection

Go's `encoding/json` struct decoding normalizes keys with `strings.EqualFold` when matching struct fields. This means a request body like `{"Method":"tools/call"}` is decoded as `method = "tools/call"` by struct-based decoders (used by mcp-go and the official go-sdk), even though a map-based decoder sees `Method` as absent from the conventional field set. A map-based inspector and a struct-based upstream therefore disagree on what was sent.

To prevent this parser-differential, the proxy rejects any key that EqualFold-matches a significant field name without being byte-identical to it. Affected field names: top-level `jsonrpc`, `id`, `method`, `params`; params-level `name`, `arguments`; argument-level keys when the tool has `args` constraints. The rejection is fail-closed (HTTP 403, `-32600`).

## Path matching rules

- All requests on a policy host must arrive at a canonical path. Non-canonical paths — percent-encoded segments, double slashes, traversal dots — are denied (`-32600`) before inspection.
- When a policy entry specifies `path`, matching is case-insensitive with trailing slash ignored (`/mcp` matches `/mcp/`).
- A `path` value must start with `/`.

## Method coverage

The policy inspector fires on all HTTP methods except GET, HEAD, and OPTIONS. Non-GET/HEAD/OPTIONS requests without a valid JSON body are denied. This prevents bypasses via PUT or other non-standard verbs: every method that can carry a JSON-RPC body receives the same body inspection as POST.

## Host normalization

`MCPPolicies` keys are lowercased and trailing dots stripped at proxy construction time (`New`). `mcp.test.` and `MCP.TEST` both normalize to `mcp.test`. The same normalization applies in `buildMCPPolicies` (service layer) and `--egress-mcp-json` CLI parsing, so a miscased or dot-terminated hostname in any input surface resolves to the same canonical key.
