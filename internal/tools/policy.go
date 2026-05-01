package tools

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CapabilityPolicy controls which capabilities an agent is allowed to exercise.
// A nil *CapabilityPolicy is fully permissive (backward compatible with no config).
type CapabilityPolicy struct {
	Shell                 bool     // Allow shell execution tools (local, ssh). Default: true.
	Network               bool     // Allow network tools (web_fetch, search, browser). Default: true.
	NetworkAllowlist      []string // Allowed public hostnames when Network is true. Empty = all public hosts allowed.
	AllowInternalNetworks bool     // Allow loopback/private/link-local IPs even when they would normally be blocked.
	Cron                  bool     // Allow cron scheduling. Default: true.
	MemoryWrite           bool     // Allow memory write tools. Default: true. Ready for future memory_capture tools.
	Spawn                 bool     // Allow sub-agent/job spawning (browser_task). Default: true.
	FilesystemRoots       []string // Allowed absolute filesystem paths. Empty = no restriction.
	FileReadOnly          bool     // Deny file/patch write operations.
}

// capabilitiesForTool maps tool names to the capabilities that govern them.
// A tool requires ALL listed capabilities to be allowed.
var capabilitiesForTool = map[string][]string{
	"local":           {"shell"},
	"ssh":             {"shell"},
	"web_fetch":       {"network"},
	"search":          {"network"},
	"browser":         {"network"},
	"frontend_verify": {"network"},
	"browser_task":    {"network", "spawn"},
	"cron":            {"cron"},
}

// CapabilityForTool returns the capabilities governing the named tool.
// Returns nil if the tool is not governed by any capability.
func CapabilityForTool(toolName string) []string {
	caps, ok := capabilitiesForTool[toolName]
	if !ok {
		return nil
	}
	out := make([]string, len(caps))
	copy(out, caps)
	return out
}

// IsAllowed reports whether the named capability is permitted.
func (p *CapabilityPolicy) IsAllowed(capability string) bool {
	switch capability {
	case "shell":
		return p.Shell
	case "network":
		return p.Network
	case "cron":
		return p.Cron
	case "memory_write":
		return p.MemoryWrite
	case "spawn":
		return p.Spawn
	default:
		return true
	}
}

// DeniedCapability returns the first denied capability for the named tool,
// or "" if the tool is fully allowed by capability checks.
func (p *CapabilityPolicy) DeniedCapability(toolName string) string {
	caps, ok := capabilitiesForTool[toolName]
	if !ok {
		return ""
	}
	for _, cap := range caps {
		if !p.IsAllowed(cap) {
			return cap
		}
	}
	return ""
}

// ApplyPolicy returns a new registry with tools wrapped according to the
// given capability policy. Tools denied by policy return ToolDenial on
// execution. File tools are wrapped for filesystem/write-scope restrictions.
// A nil policy returns the registry unchanged.
func ApplyPolicy(registry *Registry, policy *CapabilityPolicy) *Registry {
	if policy == nil {
		return registry
	}

	result := &Registry{tools: make(map[string]Tool)}
	for _, tool := range registry.List() {
		result.tools[tool.Name()] = wrapForPolicy(tool, policy)
	}
	return result
}

func wrapForPolicy(tool Tool, policy *CapabilityPolicy) Tool {
	name := tool.Name()

	// Boolean capability denial.
	if denied := policy.DeniedCapability(name); denied != "" {
		return wrapToolWithPolicyDenial(tool, denied)
	}

	// Network-capable tools always get the policy context. Even with an empty
	// allowlist, the policy controls internal network access and denial shape.
	switch name {
	case "web_fetch", "browser", "browser_task", "frontend_verify", "search":
		tool = wrapToolWithNetworkPolicy(tool, policy)
	}

	// File-specific restrictions.
	needsWriteGuard := (name == "file" || name == "patch") && policy.FileReadOnly
	needsRootsGuard := (name == "file" || name == "patch" || name == "grep") && len(policy.FilesystemRoots) > 0
	if needsWriteGuard || needsRootsGuard {
		return wrapToolWithFilePolicy(tool, policy)
	}

	return tool
}

// ---------------------------------------------------------------------------
// Policy denial guard — blocks the tool entirely.
// ---------------------------------------------------------------------------

type policyDenialGuard struct {
	tool       Tool
	capability string
}

func (g *policyDenialGuard) Name() string        { return g.tool.Name() }
func (g *policyDenialGuard) Description() string { return g.tool.Description() }
func (g *policyDenialGuard) Unwrap() Tool        { return g.tool }

func (g *policyDenialGuard) Execute(_ context.Context, _ ...string) (string, error) {
	return "", g.denial()
}

func (g *policyDenialGuard) denial() *ToolDenial {
	return &ToolDenial{
		ToolName:    g.tool.Name(),
		Family:      g.capability,
		Reason:      fmt.Sprintf("capability %q denied by agent policy", g.capability),
		Remediation: "Ask the operator to update the agent's capability policy.",
	}
}

// Variants that preserve ToolSchema and/or jsonExecutor interfaces.

type policyDenialGuardWithSchema struct {
	*policyDenialGuard
	schema ToolSchema
}

func (g *policyDenialGuardWithSchema) GetSchema() map[string]interface{} {
	return g.schema.GetSchema()
}

type policyDenialGuardWithJSON struct {
	*policyDenialGuard
	json jsonExecutor
}

func (g *policyDenialGuardWithJSON) ExecuteJSON(_ context.Context, _ map[string]string) (string, error) {
	return "", g.denial()
}

type policyDenialGuardWithSchemaAndJSON struct {
	*policyDenialGuard
	schema ToolSchema
	json   jsonExecutor
}

func (g *policyDenialGuardWithSchemaAndJSON) GetSchema() map[string]interface{} {
	return g.schema.GetSchema()
}

func (g *policyDenialGuardWithSchemaAndJSON) ExecuteJSON(_ context.Context, _ map[string]string) (string, error) {
	return "", g.denial()
}

func wrapToolWithPolicyDenial(tool Tool, capability string) Tool {
	base := &policyDenialGuard{tool: tool, capability: capability}
	schema, hasSchema := tool.(ToolSchema)
	jsonExec, hasJSON := tool.(jsonExecutor)

	switch {
	case hasSchema && hasJSON:
		return &policyDenialGuardWithSchemaAndJSON{
			policyDenialGuard: base,
			schema:            schema,
			json:              jsonExec,
		}
	case hasSchema:
		return &policyDenialGuardWithSchema{
			policyDenialGuard: base,
			schema:            schema,
		}
	case hasJSON:
		return &policyDenialGuardWithJSON{
			policyDenialGuard: base,
			json:              jsonExec,
		}
	default:
		return base
	}
}

// ---------------------------------------------------------------------------
// File policy guard — enforces write scope and filesystem roots.
// ---------------------------------------------------------------------------

type filePolicyGuard struct {
	tool            Tool
	readOnly        bool
	filesystemRoots []string
}

func (g *filePolicyGuard) Name() string        { return g.tool.Name() }
func (g *filePolicyGuard) Description() string { return g.tool.Description() }
func (g *filePolicyGuard) Unwrap() Tool        { return g.tool }

func (g *filePolicyGuard) Execute(ctx context.Context, args ...string) (string, error) {
	if denial := g.check(args); denial != nil {
		return "", denial
	}
	return g.tool.Execute(ctx, args...)
}

func (g *filePolicyGuard) check(args []string) *ToolDenial {
	name := g.tool.Name()

	// Write-scope check.
	if g.readOnly {
		switch name {
		case "file":
			if len(args) > 0 && args[0] == "write" {
				return &ToolDenial{
					ToolName:    name,
					Family:      "file_write",
					Reason:      "file writes denied by agent policy (read-only mode)",
					Remediation: "Ask the operator to set file_write_scope to \"full\".",
				}
			}
		case "patch":
			// Patch is always a write operation.
			return &ToolDenial{
				ToolName:    name,
				Family:      "file_write",
				Reason:      "file writes denied by agent policy (read-only mode)",
				Remediation: "Ask the operator to set file_write_scope to \"full\".",
			}
		}
	}

	// Filesystem roots check.
	if len(g.filesystemRoots) > 0 {
		var path string
		switch name {
		case "file":
			if len(args) > 1 {
				path = args[1]
			}
		case "patch":
			if len(args) > 0 {
				path = args[0]
			}
		case "grep":
			if len(args) > 1 {
				path = args[1]
			}
		}

		if path != "" && filepath.IsAbs(path) && !isPathInRoots(path, g.filesystemRoots) {
			return &ToolDenial{
				ToolName:    name,
				Family:      "filesystem",
				Reason:      fmt.Sprintf("path %q is outside allowed filesystem roots", path),
				Remediation: "Ask the operator to update filesystem_roots in the capability policy.",
			}
		}
	}

	return nil
}

// isPathInRoots reports whether the given absolute path falls under any of the roots.
func isPathInRoots(path string, roots []string) bool {
	cleanPath := filepath.Clean(path)
	for _, root := range roots {
		cleanRoot := filepath.Clean(root)
		if cleanPath == cleanRoot || strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// Variants that preserve ToolSchema interface.

type filePolicyGuardWithSchema struct {
	*filePolicyGuard
	schema ToolSchema
}

func (g *filePolicyGuardWithSchema) GetSchema() map[string]interface{} {
	return g.schema.GetSchema()
}

func wrapToolWithFilePolicy(tool Tool, policy *CapabilityPolicy) Tool {
	base := &filePolicyGuard{
		tool:            tool,
		readOnly:        policy.FileReadOnly,
		filesystemRoots: policy.FilesystemRoots,
	}

	if schema, ok := tool.(ToolSchema); ok {
		return &filePolicyGuardWithSchema{
			filePolicyGuard: base,
			schema:          schema,
		}
	}
	return base
}

// ---------------------------------------------------------------------------
// Network policy guard — enforces per-host allowlist on network-capable tools.
// ---------------------------------------------------------------------------

// networkPolicyCtxKey is the context key for propagating the network policy
// to redirect validators and other downstream checks.
type networkPolicyCtxKey struct{}

// ContextWithNetworkPolicy attaches a CapabilityPolicy to the context so that
// downstream code (e.g. redirect validators) can enforce the same allowlist.
func ContextWithNetworkPolicy(ctx context.Context, p *CapabilityPolicy) context.Context {
	return context.WithValue(ctx, networkPolicyCtxKey{}, p)
}

// NetworkPolicyFromContext retrieves the CapabilityPolicy from ctx, or nil.
func NetworkPolicyFromContext(ctx context.Context) *CapabilityPolicy {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(networkPolicyCtxKey{}).(*CapabilityPolicy)
	return p
}

// HostMatchesAllowlist reports whether host is permitted by the allowlist.
// Each entry can be an exact hostname ("github.com") or a wildcard prefix
// ("*.github.com") that matches any subdomain of that suffix.
func HostMatchesAllowlist(host string, allowlist []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pattern := range allowlist {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if strings.HasPrefix(pattern, "*.") {
			// Wildcard: *.example.com matches sub.example.com, not example.com.
			suffix := pattern[2:] // "example.com"
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
		} else {
			if host == pattern {
				return true
			}
		}
	}
	return false
}

// CheckNetworkTarget validates that the given URL is permitted by the policy.
// It checks scheme, host allowlist, and private-IP restrictions.
// Returns a *ToolDenial on violation or nil if allowed.
func CheckNetworkTarget(toolName, rawURL string, policy *CapabilityPolicy) *ToolDenial {
	if policy == nil {
		return nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return &ToolDenial{
			ToolName:    toolName,
			Family:      "network",
			Reason:      fmt.Sprintf("invalid URL: %v", err),
			Remediation: "Provide a valid http/https URL.",
		}
	}

	scheme := strings.ToLower(parsed.Scheme)
	if scheme == "file" {
		return &ToolDenial{
			ToolName:    toolName,
			Family:      "network",
			Reason:      "file:// URLs are not allowed",
			Remediation: "Use an http or https URL instead.",
		}
	}

	if scheme == "" {
		return &ToolDenial{
			ToolName:    toolName,
			Family:      "network",
			Reason:      "URL must include an http or https scheme",
			Remediation: "Use a full URL such as https://example.com/.",
		}
	}

	if scheme != "http" && scheme != "https" {
		return &ToolDenial{
			ToolName:    toolName,
			Family:      "network",
			Reason:      fmt.Sprintf("unsupported URL scheme: %s", scheme),
			Remediation: "Use an http or https URL.",
		}
	}

	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return &ToolDenial{
			ToolName:    toolName,
			Family:      "network",
			Reason:      "URL is missing a hostname",
			Remediation: "Use a full http or https URL with a hostname.",
		}
	}

	// Allowlist check.
	if len(policy.NetworkAllowlist) > 0 && !HostMatchesAllowlist(host, policy.NetworkAllowlist) {
		return &ToolDenial{
			ToolName:    toolName,
			Family:      "network",
			Reason:      fmt.Sprintf("host %q is not in the network allowlist", host),
			Remediation: "Ask the operator to add this host to network_allowlist in the capability policy.",
		}
	}

	// Private/loopback IP check — unless AllowInternalNetworks is set.
	if !policy.AllowInternalNetworks {
		if isHostPrivateOrLoopback(host) {
			return &ToolDenial{
				ToolName:    toolName,
				Family:      "network",
				Reason:      fmt.Sprintf("host %q resolves to a private/loopback address", host),
				Remediation: "Ask the operator to enable allow_internal_networks in the capability policy.",
			}
		}
	}

	return nil
}

func searchAllowlistDenial() *ToolDenial {
	return &ToolDenial{
		ToolName:    "search",
		Family:      "network",
		Reason:      "search cannot guarantee results stay within the network allowlist",
		Remediation: "Remove the network_allowlist restriction or use web_fetch on specific allowed URLs.",
	}
}

func browserTaskAllowlistDenial() *ToolDenial {
	return &ToolDenial{
		ToolName:    "browser_task",
		Family:      "network",
		Reason:      "browser_task cannot guarantee navigation stays within the network allowlist",
		Remediation: "Remove the network_allowlist restriction or use the browser tool directly on allowed URLs.",
	}
}

// isHostPrivateOrLoopback checks if the hostname is a known loopback name or
// resolves to a private/loopback IP address.
//
// NOTE: This is used as a pre-flight check in CheckNetworkTarget. For HTTP
// requests made via web_fetch, the real enforcement happens at the transport
// layer via SSRFSafeTransport, which inspects the resolved IP at dial time
// to eliminate the DNS-rebinding TOCTOU window.
func isHostPrivateOrLoopback(host string) bool {
	lower := strings.ToLower(host)
	if lower == "localhost" || lower == "0.0.0.0" || lower == "::1" || lower == "[::1]" {
		return true
	}
	if strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".local") {
		return true
	}

	// Try parsing as an IP literal first.
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateIP(ip)
	}

	// DNS resolution — check all resolved addresses.
	ips, err := net.LookupIP(host)
	if err != nil {
		return false // can't resolve = not private
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return true
		}
	}
	return false
}

// SSRFSafeTransport returns an *http.Transport with a custom DialContext that
// checks every resolved IP address against the private/loopback blocklist
// immediately before connecting. This eliminates the DNS-rebinding TOCTOU
// window that exists when checking IPs at policy-evaluation time only.
//
// When allowInternal is true, the IP check is skipped (matching the behaviour
// of AllowInternalNetworks in CapabilityPolicy).
func SSRFSafeTransport(allowInternal bool) *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}
		if !allowInternal {
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("DNS resolution failed for %q: %w", host, err)
			}
			for _, ipAddr := range ips {
				if isPrivateIP(ipAddr.IP) {
					return nil, fmt.Errorf("connection to private/loopback IP %s blocked (SSRF protection)", ipAddr.IP)
				}
			}
			// Connect to the first resolved IP directly to prevent
			// a second resolution from hitting a different record.
			if len(ips) > 0 {
				addr = net.JoinHostPort(ips[0].IP.String(), port)
			}
		}
		return dialer.DialContext(ctx, network, addr)
	}
	return transport
}

// networkPolicyGuard wraps a network-capable tool to enforce the allowlist.
type networkPolicyGuard struct {
	tool   Tool
	policy *CapabilityPolicy
}

func (g *networkPolicyGuard) Name() string        { return g.tool.Name() }
func (g *networkPolicyGuard) Description() string { return g.tool.Description() }
func (g *networkPolicyGuard) Unwrap() Tool        { return g.tool }

func (g *networkPolicyGuard) Execute(ctx context.Context, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if denial := g.checkExecArgs(args); denial != nil {
		return "", denial
	}
	ctx = ContextWithNetworkPolicy(ctx, g.policy)
	return g.tool.Execute(ctx, args...)
}

func (g *networkPolicyGuard) checkExecArgs(args []string) *ToolDenial {
	name := g.tool.Name()

	switch name {
	case "web_fetch":
		if len(args) > 0 {
			return CheckNetworkTarget(name, args[0], g.policy)
		}
	case "browser":
		// Check URL in "open <url>" and "navigate <url>" commands.
		if len(args) >= 2 {
			cmd := strings.ToLower(args[0])
			if cmd == "open" || cmd == "navigate" || cmd == "start" {
				return CheckNetworkTarget(name, args[1], g.policy)
			}
		}
	case "frontend_verify":
		if len(args) > 0 {
			return CheckNetworkTarget(name, args[0], g.policy)
		}
	case "search":
		// Search engines make requests to their own APIs; we cannot guarantee
		// that result URLs will stay within the allowlist. When an allowlist is
		// active, deny the search tool with clear remediation.
		if len(g.policy.NetworkAllowlist) > 0 {
			return searchAllowlistDenial()
		}
	case "browser_task":
		// browser_task delegates to a subagent with free-text instructions so
		// we cannot pre-validate URLs on the positional-args path either. Deny
		// when an explicit allowlist is configured.
		if len(g.policy.NetworkAllowlist) > 0 {
			return browserTaskAllowlistDenial()
		}
	}
	return nil
}

// Variants that preserve ToolSchema and/or jsonExecutor interfaces.

type networkPolicyGuardWithSchema struct {
	*networkPolicyGuard
	schema ToolSchema
}

func (g *networkPolicyGuardWithSchema) GetSchema() map[string]interface{} {
	return g.schema.GetSchema()
}

type networkPolicyGuardWithJSON struct {
	*networkPolicyGuard
	json jsonExecutor
}

func (g *networkPolicyGuardWithJSON) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if denial := g.checkJSONParams(params); denial != nil {
		return "", denial
	}
	ctx = ContextWithNetworkPolicy(ctx, g.policy)
	return g.json.ExecuteJSON(ctx, params)
}

func (g *networkPolicyGuardWithJSON) checkJSONParams(params map[string]string) *ToolDenial {
	name := g.tool.Name()
	switch name {
	case "browser":
		cmd := strings.ToLower(params["command"])
		if cmd == "open" || cmd == "navigate" || cmd == "start" {
			if u := params["url"]; u != "" {
				return CheckNetworkTarget(name, u, g.policy)
			}
		}
	case "browser_task":
		// browser_task delegates to a subagent that uses browser; the task is
		// a free-text description so we cannot pre-validate URLs. The subagent
		// will inherit the policy through context and enforce it per-navigation.
		if len(g.policy.NetworkAllowlist) > 0 {
			return browserTaskAllowlistDenial()
		}
	case "frontend_verify":
		if u := params["url"]; u != "" {
			return CheckNetworkTarget(name, u, g.policy)
		}
	case "search":
		if len(g.policy.NetworkAllowlist) > 0 {
			return searchAllowlistDenial()
		}
	}
	return nil
}

type networkPolicyGuardWithSchemaAndJSON struct {
	*networkPolicyGuard
	schema ToolSchema
	json   jsonExecutor
}

func (g *networkPolicyGuardWithSchemaAndJSON) GetSchema() map[string]interface{} {
	return g.schema.GetSchema()
}

func (g *networkPolicyGuardWithSchemaAndJSON) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if denial := g.checkJSONParams(params); denial != nil {
		return "", denial
	}
	ctx = ContextWithNetworkPolicy(ctx, g.policy)
	return g.json.ExecuteJSON(ctx, params)
}

func (g *networkPolicyGuardWithSchemaAndJSON) checkJSONParams(params map[string]string) *ToolDenial {
	// Delegate to the embedded guard's method pattern.
	wrapped := &networkPolicyGuardWithJSON{networkPolicyGuard: g.networkPolicyGuard, json: g.json}
	return wrapped.checkJSONParams(params)
}

func wrapToolWithNetworkPolicy(tool Tool, policy *CapabilityPolicy) Tool {
	base := &networkPolicyGuard{tool: tool, policy: policy}
	schema, hasSchema := tool.(ToolSchema)
	jsonExec, hasJSON := tool.(jsonExecutor)

	switch {
	case hasSchema && hasJSON:
		return &networkPolicyGuardWithSchemaAndJSON{
			networkPolicyGuard: base,
			schema:             schema,
			json:               jsonExec,
		}
	case hasSchema:
		return &networkPolicyGuardWithSchema{
			networkPolicyGuard: base,
			schema:             schema,
		}
	case hasJSON:
		return &networkPolicyGuardWithJSON{
			networkPolicyGuard: base,
			json:               jsonExec,
		}
	default:
		return base
	}
}
