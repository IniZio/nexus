package mitm

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/elazarl/goproxy"

	"github.com/IniZio/nexus/internal/core/domain"
)

const maxMCPBody = 1 << 20
const maxMCPSSEBuf = 4 << 20

// MCPPolicy restricts MCP JSON-RPC tool calls for one host.
type MCPPolicy struct {
	Path  string
	Allow []string
	Args  map[string]map[string]string
}

// ValidateMCPPolicy returns an error if p is malformed: empty Allow entries, Args
// keys absent from Allow, or invalid path.Match glob patterns in Args.
func ValidateMCPPolicy(p MCPPolicy) error {
	if len(p.Allow) == 0 {
		return fmt.Errorf("mitm: MCPPolicy Allow must be non-empty")
	}
	for _, tool := range p.Allow {
		if tool == "" {
			return fmt.Errorf("mitm: MCPPolicy Allow contains empty tool name")
		}
	}
	for tool, argGlobs := range p.Args {
		if !slices.Contains(p.Allow, tool) {
			return fmt.Errorf("mitm: MCPPolicy Args key %q not in Allow", tool)
		}
		for argName, pat := range argGlobs {
			if _, err := path.Match(pat, ""); err != nil {
				return fmt.Errorf("mitm: MCPPolicy Args[%q][%q]: invalid glob: %w", tool, argName, err)
			}
		}
	}
	if p.Path != "" && !strings.HasPrefix(p.Path, "/") {
		return fmt.Errorf("mitm: MCPPolicy Path must start with /")
	}
	return nil
}

func foldKeyConflict(data []byte, significant ...string) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return false
	}
	seen := make([]string, 0, 8)
	for dec.More() {
		tok, err = dec.Token()
		if err != nil {
			return false
		}
		key, ok := tok.(string)
		if !ok {
			return false
		}
		for _, sig := range significant {
			if strings.EqualFold(key, sig) && key != sig {
				return true
			}
		}
		for _, prev := range seen {
			if strings.EqualFold(key, prev) {
				return true
			}
		}
		seen = append(seen, key)
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			return false
		}
	}
	return false
}

type mcpToolsListCtx struct {
	policy MCPPolicy
	host   string
}

func registerMCPHandlers(
	inner *goproxy.ProxyHttpServer,
	policies map[string]MCPPolicy,
	sandboxID domain.SandboxID,
	log *slog.Logger,
	onEgress func(host, verdict, reason string, ts time.Time),
) {
	inner.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		m := strings.ToUpper(req.Method)
		if m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions {
			return req, nil
		}
		host := strings.ToLower(reqHost(req))
		pol, ok := policies[host]
		if !ok {
			return req, nil
		}

		deny403 := func(reason string, code int) (*http.Request, *http.Response) {
			log.Info("mitm: MCP request denied", "sandbox", sandboxID, "host", host, "reason", reason)
			if onEgress != nil {
				onEgress(host, "deny", "mcp: "+reason, time.Now())
			}
			return req, mcpFailClosedResponse(req, reason, code)
		}

		checkPath := req.URL.Path
		checkEscaped := req.URL.EscapedPath()
		if len(checkPath) > 1 {
			checkPath = strings.TrimSuffix(checkPath, "/")
			checkEscaped = strings.TrimSuffix(checkEscaped, "/")
		}
		if !isCanonicalPath(checkPath, checkEscaped) {
			return deny403("non-canonical request path", -32600)
		}
		if pol.Path != "" && !strings.EqualFold(strings.TrimSuffix(req.URL.Path, "/"), strings.TrimSuffix(pol.Path, "/")) {
			return req, nil
		}

		// Reject multi-value headers to prevent smuggling via header stacking.
		if len(req.Header.Values("Content-Encoding")) > 1 {
			return deny403("multiple Content-Encoding values", -32600)
		}
		if len(req.Header.Values("Content-Type")) > 1 {
			return deny403("multiple Content-Type values", -32600)
		}

		enc := strings.ToLower(req.Header.Get("Content-Encoding"))
		if enc != "" && enc != "identity" && enc != "gzip" {
			return deny403("unsupported Content-Encoding: "+req.Header.Get("Content-Encoding"), -32600)
		}

		if req.Body == nil {
			return deny403("empty body", -32600)
		}
		limited := io.LimitReader(req.Body, maxMCPBody+1)
		rawBody, err := io.ReadAll(limited)
		req.Body = io.NopCloser(bytes.NewReader(rawBody))
		if err != nil || int64(len(rawBody)) > maxMCPBody {
			return deny403("request body exceeds size limit", -32600)
		}

		mediaType, _, ctErr := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if ctErr != nil || mediaType != "application/json" {
			return deny403("Content-Type must be application/json", -32600)
		}

		inspectBody := rawBody
		if enc == "gzip" {
			decoded, gzErr := mcpDecodeGzip(rawBody)
			if gzErr != nil {
				return deny403("gzip decode failed", -32600)
			}
			inspectBody = decoded
		}

		trimmed := bytes.TrimSpace(inspectBody)
		if len(trimmed) == 0 {
			return deny403("empty JSON body", -32600)
		}

		switch trimmed[0] {
		case '{':
			ev := evalOneMCPMsg(trimmed, pol, host)
			if ev.denyReason != "" {
				log.Info("mitm: MCP request denied", "sandbox", sandboxID, "host", host, "reason", ev.denyReason)
				if onEgress != nil {
					onEgress(host, "deny", "mcp: "+ev.denyReason, time.Now())
				}
				if ev.failClosed {
					return req, mcpFailClosedResponse(req, ev.denyReason, ev.code)
				}
				return req, mcpPolicyDenyResponse(req, ev.id, ev.denyReason, ev.code)
			}
			if ev.isToolsList {
				ctx.UserData = &mcpToolsListCtx{policy: pol, host: host}
			}
			return req, nil
		case '[':
			resp, hasToolsList := evalMCPBatch(req, trimmed, pol, host, sandboxID, log, onEgress)
			if resp != nil {
				return req, resp
			}
			if hasToolsList {
				ctx.UserData = &mcpToolsListCtx{policy: pol, host: host}
			}
			return req, nil
		default:
			return deny403("JSON body must be object or array", -32600)
		}
	})

	inner.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		tlCtx, ok := ctx.UserData.(*mcpToolsListCtx)
		if !ok || resp == nil {
			return resp
		}
		return rewriteToolsListResponse(resp, tlCtx.policy)
	})
}

// msgEval is the result of evaluating one JSON-RPC message.
type msgEval struct {
	id          json.RawMessage
	denyReason  string
	code        int
	failClosed  bool
	isToolsList bool
}

// evalOneMCPMsg evaluates a single JSON-RPC object against pol.
func evalOneMCPMsg(data []byte, pol MCPPolicy, host string) msgEval {
	if hasDuplicateTopLevelJSONKeys(data) || foldKeyConflict(data, "jsonrpc", "id", "method", "params") {
		return msgEval{denyReason: "duplicate or fold-ambiguous top-level keys", code: -32600, failClosed: true}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return msgEval{denyReason: "parse error", code: -32700, failClosed: true}
	}
	id := raw["id"]

	methodRaw, hasMeth := raw["method"]
	if !hasMeth {
		return msgEval{id: id}
	}
	var method string
	if err := json.Unmarshal(methodRaw, &method); err != nil {
		return msgEval{denyReason: "method is not a string", code: -32600, failClosed: true}
	}
	if method == "tools/list" {
		return msgEval{id: id, isToolsList: true}
	}
	if method != "tools/call" {
		return msgEval{id: id}
	}

	paramsRaw, hasParams := raw["params"]
	if !hasParams || len(bytes.TrimSpace(paramsRaw)) == 0 {
		reason := fmt.Sprintf("tool %q is not allowed on host %s", "", host)
		return msgEval{id: id, denyReason: reason, code: -32001}
	}
	if hasDuplicateTopLevelJSONKeys(paramsRaw) || foldKeyConflict(paramsRaw, "name", "arguments") {
		return msgEval{id: id, denyReason: "duplicate or fold-ambiguous params keys", code: -32600, failClosed: true}
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		return msgEval{denyReason: "parse error in params", code: -32700, failClosed: true}
	}
	nameRaw, hasName := params["name"]
	if !hasName {
		reason := fmt.Sprintf("tool %q is not allowed on host %s", "", host)
		return msgEval{id: id, denyReason: reason, code: -32001}
	}
	var name string
	if err := json.Unmarshal(nameRaw, &name); err != nil {
		return msgEval{id: id, denyReason: "params.name is not a string", code: -32001}
	}

	reason, code := checkMCPToolPolicy(name, params["arguments"], pol, host)
	if reason != "" {
		return msgEval{id: id, denyReason: reason, code: code}
	}
	return msgEval{id: id}
}

func evalMCPBatch(
	req *http.Request,
	data []byte,
	pol MCPPolicy,
	host string,
	sandboxID domain.SandboxID,
	log *slog.Logger,
	onEgress func(host, verdict, reason string, ts time.Time),
) (*http.Response, bool) {
	var batch []json.RawMessage
	if err := json.Unmarshal(data, &batch); err != nil || len(batch) == 0 {
		reason := "empty or unparseable batch"
		log.Info("mitm: MCP batch denied", "sandbox", sandboxID, "host", host, "reason", reason)
		if onEgress != nil {
			onEgress(host, "deny", "mcp: "+reason, time.Now())
		}
		return mcpFailClosedResponse(req, reason, -32600), false
	}

	results := make([]msgEval, len(batch))
	anyDenied := false
	hasToolsList := false
	for i, elem := range batch {
		ev := evalOneMCPMsg(elem, pol, host)
		results[i] = ev
		if ev.denyReason != "" {
			anyDenied = true
		}
		if ev.isToolsList {
			hasToolsList = true
		}
	}
	if !anyDenied {
		return nil, hasToolsList
	}

	log.Info("mitm: MCP batch denied", "sandbox", sandboxID, "host", host)
	if onEgress != nil {
		onEgress(host, "deny", "mcp: batch contains denied tool call", time.Now())
	}

	hasID := false
	for _, r := range results {
		if len(r.id) > 0 && string(r.id) != "null" {
			hasID = true
			break
		}
	}
	if !hasID {
		return mcpFailClosedResponse(req, "batch rejected: no identifiable requests", -32600), false
	}

	var errObjs []json.RawMessage
	for _, r := range results {
		if len(r.id) == 0 || string(r.id) == "null" {
			continue
		}
		msg := "batch rejected: contains a denied tool call"
		if r.denyReason != "" {
			msg = "nexus egress policy: " + r.denyReason
		}
		code := r.code
		if code == 0 {
			code = -32001
		}
		obj, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      r.id,
			"error":   map[string]any{"code": code, "message": msg},
		})
		errObjs = append(errObjs, obj)
	}

	body, _ := json.Marshal(errObjs)
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, false
}

func checkMCPToolPolicy(toolName string, argsRaw json.RawMessage, pol MCPPolicy, host string) (string, int) {
	if !slices.Contains(pol.Allow, toolName) {
		return fmt.Sprintf("tool %q is not allowed on host %s", toolName, host), -32001
	}
	argConstraints, hasConstraints := pol.Args[toolName]
	if !hasConstraints || len(argConstraints) == 0 {
		return "", 0
	}
	if len(argConstraints) > 0 {
		argNames := make([]string, 0, len(argConstraints))
		for k := range argConstraints {
			argNames = append(argNames, k)
		}
		if hasDuplicateTopLevelJSONKeys(argsRaw) || foldKeyConflict(argsRaw, argNames...) {
			return "fold-ambiguous or duplicate argument keys", -32600
		}
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		for argName := range argConstraints {
			return fmt.Sprintf("argument %q missing or not an object", argName), -32001
		}
	}
	for argName, pat := range argConstraints {
		raw, ok := args[argName]
		if !ok {
			return fmt.Sprintf("argument %q is required by policy", argName), -32001
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Sprintf("argument %q must be a string", argName), -32001
		}
		if matched, _ := path.Match(pat, s); !matched {
			return fmt.Sprintf("argument %q does not match required pattern", argName), -32001
		}
	}
	return "", 0
}

func mcpDecodeGzip(src []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, maxMCPBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > maxMCPBody {
		return nil, fmt.Errorf("decompressed body exceeds size limit")
	}
	return out, nil
}

func mcpFailClosedResponse(req *http.Request, reason string, code int) *http.Response {
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      nil,
		"error":   map[string]any{"code": code, "message": "nexus egress policy: " + reason},
	})
	return &http.Response{
		StatusCode:    http.StatusForbidden,
		Status:        "403 Forbidden",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

func mcpPolicyDenyResponse(req *http.Request, id json.RawMessage, reason string, code int) *http.Response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": "nexus egress policy: " + reason},
	})
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

func rewriteToolsListResponse(resp *http.Response, pol MCPPolicy) *http.Response {
	if ce := resp.Header.Get("Content-Encoding"); ce != "" && strings.ToLower(ce) != "identity" {
		return resp
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch ct {
	case "application/json":
		return rewriteToolsListJSON(resp, pol)
	case "text/event-stream":
		return rewriteToolsListSSE(resp, pol)
	}
	return resp
}

func rewriteToolsListJSON(resp *http.Response, pol MCPPolicy) *http.Response {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPSSEBuf+1))
	resp.Body.Close()
	if err != nil || int64(len(body)) > maxMCPSSEBuf {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}
	if rewritten := filterToolsListBody(body, pol); rewritten != nil {
		resp.Body = io.NopCloser(bytes.NewReader(rewritten))
		resp.ContentLength = int64(len(rewritten))
		resp.Header.Del("Content-Length")
		return resp
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp
}

func rewriteToolsListSSE(resp *http.Response, pol MCPPolicy) *http.Response {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPSSEBuf+1))
	resp.Body.Close()
	if err != nil || int64(len(body)) > maxMCPSSEBuf {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}
	var out bytes.Buffer
	for line := range strings.Lines(string(body)) {
		stripped := strings.TrimRight(line, "\r\n")
		lineEnd := line[len(stripped):]
		if data, ok := strings.CutPrefix(stripped, "data:"); ok {
			data = strings.TrimPrefix(data, " ")
			if rewritten := filterToolsListBody([]byte(data), pol); rewritten != nil {
				out.WriteString("data: ")
				out.Write(rewritten)
				out.WriteString(lineEnd)
				continue
			}
		}
		out.WriteString(line)
		if lineEnd == "" {
			out.WriteByte('\n')
		}
	}
	b := out.Bytes()
	resp.Body = io.NopCloser(bytes.NewReader(b))
	resp.ContentLength = int64(len(b))
	resp.Header.Del("Content-Length")
	return resp
}

func filterToolsListBody(data []byte, pol MCPPolicy) []byte {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil
	}
	allowSet := make(map[string]bool, len(pol.Allow))
	for _, t := range pol.Allow {
		allowSet[t] = true
	}
	switch trimmed[0] {
	case '{':
		out, changed := filterToolsListObject(trimmed, allowSet)
		if !changed {
			return nil
		}
		return out
	case '[':
		var batch []json.RawMessage
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			return nil
		}
		changed := false
		for i, elem := range batch {
			if rw, c := filterToolsListObject(elem, allowSet); c {
				batch[i] = rw
				changed = true
			}
		}
		if !changed {
			return nil
		}
		out, _ := json.Marshal(batch)
		return out
	}
	return nil
}

func filterToolsListObject(data []byte, allowSet map[string]bool) ([]byte, bool) {
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return data, false
	}
	resultRaw, ok := msg["result"]
	if !ok {
		return data, false
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return data, false
	}
	toolsRaw, ok := result["tools"]
	if !ok {
		return data, false
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(toolsRaw, &tools); err != nil {
		return data, false
	}
	filtered := tools[:0:0]
	for _, t := range tools {
		var toolObj map[string]json.RawMessage
		if err := json.Unmarshal(t, &toolObj); err != nil {
			filtered = append(filtered, t)
			continue
		}
		var name string
		if err := json.Unmarshal(toolObj["name"], &name); err != nil || allowSet[name] {
			filtered = append(filtered, t)
		}
	}
	if len(filtered) == len(tools) {
		return data, false
	}
	newTools, _ := json.Marshal(filtered)
	result["tools"] = newTools
	newResult, _ := json.Marshal(result)
	msg["result"] = newResult
	out, _ := json.Marshal(msg)
	return out, true
}
