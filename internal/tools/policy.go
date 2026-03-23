package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CapabilityPolicy controls which capabilities an agent is allowed to exercise.
// A nil *CapabilityPolicy is fully permissive (backward compatible with no config).
type CapabilityPolicy struct {
	Shell            bool     // Allow shell execution tools (local, ssh). Default: true.
	Network          bool     // Allow network tools (web_fetch, search, browser). Default: true.
	NetworkAllowlist []string // Allowed hostnames when Network is true. Empty = all. Future per-request enforcement.
	Cron             bool     // Allow cron scheduling. Default: true.
	MemoryWrite      bool     // Allow memory write tools. Default: true. Ready for future memory_capture tools.
	Spawn            bool     // Allow sub-agent/job spawning (browser_task). Default: true.
	FilesystemRoots  []string // Allowed absolute filesystem paths. Empty = no restriction.
	FileReadOnly     bool     // Deny file/patch write operations.
}

// capabilitiesForTool maps tool names to the capabilities that govern them.
// A tool requires ALL listed capabilities to be allowed.
var capabilitiesForTool = map[string][]string{
	"local":        {"shell"},
	"ssh":          {"shell"},
	"web_fetch":    {"network"},
	"search":       {"network"},
	"browser":      {"network"},
	"browser_task": {"network", "spawn"},
	"cron":         {"cron"},
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
