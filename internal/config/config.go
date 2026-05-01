package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"

	"ok-gobot/internal/bootstrap"
)

// DefaultModelAliases provides shorthand names for popular models.
// Users can type `/model sonnet` instead of the full model identifier.
var DefaultModelAliases = map[string]string{
	"sonnet":   "claude-sonnet-4-5-20250929",
	"opus":     "claude-opus-4-5-20251101",
	"haiku":    "claude-haiku-3-5-20241022",
	"gpt4":     "openai/gpt-4o",
	"gpt4m":    "openai/gpt-4o-mini",
	"kimi":     "moonshotai/kimi-k2.5",
	"gemini":   "google/gemini-2.5-pro",
	"flash":    "google/gemini-2.5-flash",
	"deepseek": "deepseek/deepseek-chat-v3-0324",
	"glm":      "glm-5",
	"minimax":  "minimax-m2.5",
}

// ControlConfig holds configuration for the loopback WebSocket control server.
type ControlConfig struct {
	Enabled                   bool   `mapstructure:"enabled"`
	Port                      int    `mapstructure:"port"`
	Token                     string `mapstructure:"token"`
	AllowLoopbackWithoutToken bool   `mapstructure:"allow_loopback_without_token"`
}

// RuntimeConfig holds runtime mailbox configuration.
type RuntimeConfig struct {
	// Mode is a legacy compatibility knob retained so older configs still decode.
	// It is ignored: the active architecture contract is the chat/jobs mailbox runtime.
	// Supported legacy values remain "", "hub", and "legacy" while removal is pending.
	Mode string `mapstructure:"mode"`
	// SessionQueueLimit is the per-session queue capacity for chat/jobs mailbox execution.
	// 0 falls back to runtime defaults where applicable for the active runtime path.
	SessionQueueLimit int `mapstructure:"session_queue_limit"`
	// CostTiers defines global execution settings per cost tier.
	// Keys are tier names: "premium", "standard", "cheap", "local".
	CostTiers map[string]CostTierEntry `mapstructure:"cost_tiers"`
	// Roles defines named role policies that map work to cost tiers.
	Roles []RolePolicyEntry `mapstructure:"roles"`
}

// CostTierEntry describes the execution settings for one cost tier in configuration.
type CostTierEntry struct {
	Model        string `mapstructure:"model"`
	Provider     string `mapstructure:"provider"`
	BaseURL      string `mapstructure:"base_url"`
	Thinking     string `mapstructure:"thinking"`
	MaxToolCalls int    `mapstructure:"max_tool_calls"`
	MaxDuration  string `mapstructure:"max_duration"` // parseable duration, e.g. "5m"
}

// RolePolicyEntry describes how a named role routes work across cost tiers.
type RolePolicyEntry struct {
	Name        string                   `mapstructure:"name"`
	DefaultTier string                   `mapstructure:"default_tier"`
	Tiers       map[string]CostTierEntry `mapstructure:"tiers"`
}

// BrowserConfig holds browser automation settings.
type BrowserConfig struct {
	ChromePath  string `mapstructure:"chrome_path"`  // explicit path to Chrome/Chromium binary
	ProfilePath string `mapstructure:"profile_path"` // user data directory for browser profiles
	DebugURL    string `mapstructure:"debug_url"`    // connect to existing browser (e.g. http://127.0.0.1:9222)
}

// ArtifactConfig holds local proof artifact display settings.
type ArtifactConfig struct {
	Roots []string `mapstructure:"roots"` // Local roots safe for Mission Control/API artifact previews
}

// SessionConfig holds session-key derivation behavior.
type SessionConfig struct {
	// DMScope controls how DM session keys are created:
	// "main" (default): shared main session
	// "per_user": one session per Telegram user
	DMScope string `mapstructure:"dm_scope"`
}

// WorktreeConfig holds configuration for auto-worktree management.
type WorktreeConfig struct {
	BaseDir      string `mapstructure:"base_dir"`       // Base directory for worktrees (default: ~/worktrees/<reponame>)
	StaleAgeDays int    `mapstructure:"stale_age_days"` // Days after which a worktree is considered stale (default: 7)
	Worker       string `mapstructure:"worker"`         // Worker binary to spawn in worktree: "claude" (default) or "codex"
	Model        string `mapstructure:"model"`          // Model override for worker
}

// MaestroConfig holds strict GitHub issue intake settings for worker selection.
type MaestroConfig struct {
	Repo              string   `mapstructure:"repo"`                // GitHub repo, e.g. owner/name. Empty lets gh infer from cwd.
	ReadyLabel        string   `mapstructure:"ready_label"`         // Required label for default eligibility.
	HardExcludeLabels []string `mapstructure:"hard_exclude_labels"` // Labels that block intake by default.
	Limit             int      `mapstructure:"limit"`               // Number of open issues to inspect in dry-runs/status.
}

// EvolutionConfig holds self-evolution loop configuration.
type EvolutionConfig struct {
	Enabled        bool    `mapstructure:"enabled"`          // Enable automatic self-evolution (default false)
	TasksPerCycle  int     `mapstructure:"tasks_per_cycle"`  // Tasks before triggering evolution analysis (default 50)
	PassThreshold  float64 `mapstructure:"pass_threshold"`   // Benchmark pass rate to promote candidate (default 0.8)
	MaxDiffPercent float64 `mapstructure:"max_diff_percent"` // Prompt diff % requiring human approval (default 0.2)
	BenchmarksDir  string  `mapstructure:"benchmarks_dir"`   // Directory containing benchmark task files (default: ./benchmarks)
	EvolutionDir   string  `mapstructure:"evolution_dir"`    // Directory for versioned agent configs
}

// Config holds all application configuration
type Config struct {
	ConfigPath   string            `mapstructure:"-"`
	Telegram     TelegramConfig    `mapstructure:"telegram"`
	AI           AIConfig          `mapstructure:"ai"`
	Auth         AuthConfig        `mapstructure:"auth"`
	API          APIConfig         `mapstructure:"api"`
	Control      ControlConfig     `mapstructure:"control"`
	Browser      BrowserConfig     `mapstructure:"browser"`
	Artifacts    ArtifactConfig    `mapstructure:"artifacts"`
	Runtime      RuntimeConfig     `mapstructure:"runtime"`
	Session      SessionConfig     `mapstructure:"session"`
	Groups       GroupsConfig      `mapstructure:"groups"`
	TTS          TTSConfig         `mapstructure:"tts"`
	STT          STTConfig         `mapstructure:"stt"`
	Memory       MemoryConfig      `mapstructure:"memory"`
	Worktree     WorktreeConfig    `mapstructure:"worktree"`
	Maestro      MaestroConfig     `mapstructure:"maestro"`
	Evolution    EvolutionConfig   `mapstructure:"evolution"`
	Agents       []AgentConfig     `mapstructure:"agents"`
	Models       []string          `mapstructure:"models"` // list of models for TUI/web picker
	ModelAliases map[string]string `mapstructure:"model_aliases"`
	Contacts     map[string]int64  `mapstructure:"contacts"` // alias -> chatID for message tool allowlist
	StoragePath  string            `mapstructure:"storage_path"`
	LogLevel     string            `mapstructure:"log_level"`
	SoulPath     string            `mapstructure:"soul_path"`  // Path to agent personality files (deprecated, use agents)
	RolesPath    string            `mapstructure:"roles_path"` // Directory of role manifests to auto-register on startup
}

// TelegramConfig holds Telegram bot configuration
type TelegramConfig struct {
	Token   string `mapstructure:"token"`
	Webhook string `mapstructure:"webhook"`
}

// AIConfig holds AI provider configuration.
// Supports: openrouter, openai, anthropic, droid, chatgpt (openai-codex), or custom OpenAI-compatible APIs.
type AIConfig struct {
	Provider        string             `mapstructure:"provider"` // "openrouter", "openai", "anthropic", "droid", "chatgpt", "openai-codex", "custom"
	APIKey          string             `mapstructure:"api_key"`
	Model           string             `mapstructure:"model"`
	BaseURL         string             `mapstructure:"base_url"`         // For custom providers
	FallbackModels  []string           `mapstructure:"fallback_models"`  // Models to try if primary fails
	DefaultThinking string             `mapstructure:"default_thinking"` // Default thinking level: "off", "low", "medium", "high", "adaptive"
	Routing         ModelRoutingConfig `mapstructure:"routing"`          // Per-task-type model routing
	Droid           DroidConfig        `mapstructure:"droid"`            // Droid-specific settings (provider=droid)
}

// ModelRoutingConfig holds per-task-type model routing configuration.
// Routes maps task type names to model identifiers. Recognized types are:
// "vision", "summarize", "reasoning", "coding", "default".
// When a task type is not found in Routes the global ai.model is used as fallback.
// Example:
//
//	routing:
//	  routes:
//	    vision:    "openai/gpt-4o"
//	    summarize: "openai/gpt-4o-mini"
//	    reasoning: "anthropic/claude-opus-4-5-20251101"
//	    coding:    "moonshotai/kimi-k2.5"
type ModelRoutingConfig struct {
	Routes map[string]string `mapstructure:"routes"`
}

// DroidConfig holds configuration for the factory.ai droid provider.
type DroidConfig struct {
	BinaryPath string `mapstructure:"binary_path"` // Path to droid binary (default: "droid")
	AutoLevel  string `mapstructure:"auto_level"`  // Autonomy level: "", "low", "medium", "high"
	WorkDir    string `mapstructure:"work_dir"`    // Working directory for droid execution
}

// AuthConfig holds authorization configuration
type AuthConfig struct {
	Mode         string  `mapstructure:"mode"`          // "open", "allowlist", "pairing"
	AllowedUsers []int64 `mapstructure:"allowed_users"` // List of allowed Telegram user IDs
	AdminID      int64   `mapstructure:"admin_id"`      // Admin user ID who can manage auth
}

// APIConfig holds HTTP API configuration
type APIConfig struct {
	Enabled     bool   `mapstructure:"enabled"`      // Enable HTTP API server
	Port        int    `mapstructure:"port"`         // API server port
	BindAddr    string `mapstructure:"bind_addr"`    // Bind address (default "127.0.0.1")
	APIKey      string `mapstructure:"api_key"`      // Required API key for authentication
	WebhookChat int64  `mapstructure:"webhook_chat"` // Default chat ID for webhook messages
}

// GroupsConfig holds group chat configuration
type GroupsConfig struct {
	DefaultMode string `mapstructure:"default_mode"` // "active" or "standby"
}

// TTSConfig holds text-to-speech configuration
type TTSConfig struct {
	Provider     string `mapstructure:"provider"`      // "openai" or "edge"
	DefaultVoice string `mapstructure:"default_voice"` // Provider-specific default voice
}

// STTConfig holds speech-to-text (voice transcription) configuration
type STTConfig struct {
	BaseURL             string  `mapstructure:"base_url"`             // Whisper-compatible API base URL, e.g. "https://scribe.ok.labs/v1"
	APIKey              string  `mapstructure:"api_key"`              // API key (optional for local deployments)
	ConfidenceThreshold float64 `mapstructure:"confidence_threshold"` // 0.0–1.0; below this threshold a confirmation prompt is shown (default 0.6)
}

// MemoryConfig holds semantic memory configuration
type MemoryConfig struct {
	Enabled            bool                    `mapstructure:"enabled"`             // Enable semantic memory
	Mode               string                  `mapstructure:"mode"`                // Prompt assembly mode: "eager" (default), "retrieval_first", or "startup_recent"
	Backend            string                  `mapstructure:"backend"`             // Memory search backend: builtin, qmd, or auto
	EmbeddingsBaseURL  string                  `mapstructure:"embeddings_base_url"` // API base URL for embeddings
	EmbeddingsAPIKey   string                  `mapstructure:"embeddings_api_key"`  // API key for embeddings (can reuse ai.api_key)
	EmbeddingsModel    string                  `mapstructure:"embeddings_model"`    // Embeddings model to use
	MetadataExtraction bool                    `mapstructure:"metadata_extraction"` // Extract structured metadata while indexing memories
	MetadataModel      string                  `mapstructure:"metadata_model"`      // LLM model used for metadata extraction
	ExtraPaths         []MemoryExtraPathConfig `mapstructure:"extra_paths"`         // Additional named markdown roots to index (Obsidian vaults, shared exports, etc.)
	MCP                MemoryMCPConfig         `mapstructure:"mcp"`                 // Optional MCP server exposing memory tools
	QMD                MemoryQMDConfig         `mapstructure:"qmd"`                 // Optional read-only QMD-compatible backend
	Active             ActiveMemoryConfig      `mapstructure:"active"`              // Pre-reply Active Memory recall (DM only)
	Sessions           SessionMemoryConfig     `mapstructure:"sessions"`            // Session transcript indexing (off by default)
	Curation           MemoryCurationConfig    `mapstructure:"curation"`            // Optional scheduled curation suggestions (disabled by default)
}

// ActiveMemoryConfig configures the pre-reply Active Memory recall step.
// When enabled, ok-gobot calls the memory backend before the main model
// response on direct (DM) chats and injects the result as untrusted context.
type ActiveMemoryConfig struct {
	Enabled      bool `mapstructure:"enabled"`       // Enable Active Memory pre-reply recall (default: false)
	TimeoutMS    int  `mapstructure:"timeout_ms"`    // Maximum recall latency before falling back (default: 1500)
	MaxSnippets  int  `mapstructure:"max_snippets"`  // Maximum recall snippets to inject (default: 5)
	MaxChars     int  `mapstructure:"max_chars"`     // Maximum total characters of injected memory (default: 2000)
	HistoryTurns int  `mapstructure:"history_turns"` // Recent turns blended into the recall query (default: 3)
}

// MemoryExtraPathConfig describes an additional markdown root to index alongside
// the primary workspace memory. Each entry becomes a named "collection" exposed
// in the memory_search results and memory_get tool with the prefix
// "extra:<name>/...". Extra paths are read-only by default; writes to these
// roots are never performed automatically.
type MemoryExtraPathConfig struct {
	Name     string   `mapstructure:"name"`      // Collection identifier (required, [a-z0-9-_]+)
	Path     string   `mapstructure:"path"`      // Absolute or "~/..." path to the markdown root
	Patterns []string `mapstructure:"patterns"`  // Glob patterns relative to path (defaults to ["**/*.md"])
	ReadOnly *bool    `mapstructure:"read_only"` // Defaults to true; reserved for future write enablement
	Scope    string   `mapstructure:"scope"`     // Optional human-readable scope label (e.g. "obsidian", "homelab")
}

// SessionMemoryConfig controls indexing of past session transcripts as a
// retrieval source. Transcript memory is disabled by default for privacy:
// it stores user-supplied text in the search index and must be opted into
// explicitly. Group sessions are excluded unless include_groups is true.
type SessionMemoryConfig struct {
	Enabled               bool `mapstructure:"enabled"`                  // Enable indexing past session transcripts (default false)
	IncludeGroups         bool `mapstructure:"include_groups"`           // Index group-keyed sessions too (default false)
	MaxMessagesPerSession int  `mapstructure:"max_messages_per_session"` // Cap on messages indexed per session (0 = unlimited)
}

// Memory prompt mode constants. The mode controls what the bootstrap loader
// pulls into the system prompt and what guidance the agent receives about
// memory_search / memory_get usage.
const (
	// MemoryModeEager loads MEMORY.md and today's + yesterday's daily notes
	// into the system prompt. Original ok-gobot behavior; preserved as the
	// default for backward compatibility.
	MemoryModeEager = "eager"
	// MemoryModeRetrievalFirst keeps MEMORY.md in the prompt for compact
	// long-term context but omits daily notes. Pairs with strong instructions
	// to call memory_search / memory_get and cite source paths.
	MemoryModeRetrievalFirst = "retrieval_first"
	// MemoryModeStartupRecent loads MEMORY.md plus today's daily note (only)
	// for a session-start one-shot bootstrap. Yesterday's note is reachable
	// via retrieval but not eagerly injected.
	MemoryModeStartupRecent = "startup_recent"
)

// NormalizeMemoryMode returns the normalized mode name. Empty input falls
// back to MemoryModeEager.
func NormalizeMemoryMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "", MemoryModeEager:
		return MemoryModeEager
	case MemoryModeRetrievalFirst:
		return MemoryModeRetrievalFirst
	case MemoryModeStartupRecent:
		return MemoryModeStartupRecent
	default:
		return mode
	}
}

// IsValidMemoryMode reports whether mode is a recognized memory prompt mode.
func IsValidMemoryMode(mode string) bool {
	switch mode {
	case MemoryModeEager, MemoryModeRetrievalFirst, MemoryModeStartupRecent:
		return true
	default:
		return false
	}
}

// MemoryCurationConfig configures the optional scheduled curation suggestion
// loop. The suggestion mode never modifies MEMORY.md; it only generates a
// draft for admin review on the configured cadence. Default: disabled.
type MemoryCurationConfig struct {
	Enabled  bool   `mapstructure:"enabled"`  // When true, a scheduled job creates curation drafts on the configured cadence (default false)
	Schedule string `mapstructure:"schedule"` // Optional cron expression; ignored when Enabled is false
	Days     int    `mapstructure:"days"`     // Daily-note window for each suggestion run (default 7)
}

// MemoryQMDConfig holds optional QMD-compatible backend configuration.
type MemoryQMDConfig struct {
	BinaryPath       string                     `mapstructure:"binary_path"`       // qmd binary path (default: qmd)
	Index            string                     `mapstructure:"index"`             // qmd index name (default: index)
	IndexPath        string                     `mapstructure:"index_path"`        // explicit SQLite index path; avoids inferring ~/.cache/qmd
	SearchMode       string                     `mapstructure:"search_mode"`       // search, vsearch, or query
	Timeout          string                     `mapstructure:"timeout"`           // per-command timeout, e.g. 10s
	FallbackCooldown string                     `mapstructure:"fallback_cooldown"` // suppress failed QMD retries for this duration
	Collections      MemoryQMDCollectionsConfig `mapstructure:"collections"`       // pre-existing QMD collection names by memory role
}

// MemoryQMDCollectionsConfig maps ok-gobot memory roles to QMD collection names.
type MemoryQMDCollectionsConfig struct {
	Workspace          string   `mapstructure:"workspace"`
	DailyNotes         string   `mapstructure:"daily_notes"`
	SessionTranscripts string   `mapstructure:"session_transcripts"`
	ExtraPaths         []string `mapstructure:"extra_paths"`
}

// MemoryMCPConfig holds memory MCP server configuration
type MemoryMCPConfig struct {
	Enabled     bool   `mapstructure:"enabled"`      // Enable memory MCP server
	Host        string `mapstructure:"host"`         // Bind host (default loopback only)
	Port        int    `mapstructure:"port"`         // MCP server port
	Endpoint    string `mapstructure:"endpoint"`     // MCP endpoint path
	AllowWrites bool   `mapstructure:"allow_writes"` // Allow write tools such as memory_capture
}

// CapabilityPolicyConfig defines per-agent capability restrictions in config.
// All *bool fields default to true (permissive) when nil.
// A nil *CapabilityPolicyConfig on an agent means no restrictions (backward compatible).
type CapabilityPolicyConfig struct {
	Shell                 *bool    `mapstructure:"shell"`                   // Allow shell execution (local, ssh). Default: true.
	Network               *bool    `mapstructure:"network"`                 // Allow network tools (web_fetch, search, browser). Default: true.
	NetworkAllowlist      []string `mapstructure:"network_allowlist"`       // Allowed public hostnames when network is true. Empty = all public hosts.
	AllowInternalNetworks bool     `mapstructure:"allow_internal_networks"` // Allow loopback/private/link-local IPs. Default: false.
	Cron                  *bool    `mapstructure:"cron"`                    // Allow cron scheduling. Default: true.
	MemoryWrite           *bool    `mapstructure:"memory_write"`            // Allow memory write tools. Default: true.
	Spawn                 *bool    `mapstructure:"spawn"`                   // Allow sub-agent/job spawning. Default: true.
	FilesystemRoots       []string `mapstructure:"filesystem_roots"`        // Allowed absolute filesystem paths. Empty = no restriction.
	FileWriteScope        string   `mapstructure:"file_write_scope"`        // "full" (default) or "read_only".
}

// AgentConfig holds configuration for a single agent
type AgentConfig struct {
	Name         string                  `mapstructure:"name"`
	SoulPath     string                  `mapstructure:"soul_path"`
	Model        string                  `mapstructure:"model"`         // Empty = use global ai.model
	AllowedTools []string                `mapstructure:"allowed_tools"` // Empty = all tools allowed
	Capabilities *CapabilityPolicyConfig `mapstructure:"capabilities"`  // Optional fine-grained capability policy
}

// OpenAIConfig holds OpenAI API configuration (legacy, use AIConfig)
type OpenAIConfig struct {
	APIKey string `mapstructure:"api_key"`
	Model  string `mapstructure:"model"`
}

// Load reads configuration from file and environment
func Load() (*Config, error) {
	v := viper.New()

	// Set defaults
	v.SetDefault("log_level", "info")
	v.SetDefault("storage_path", "~/.ok-gobot/ok-gobot.db")
	v.SetDefault("soul_path", bootstrap.DefaultPath) // Default to visible directory
	v.SetDefault("ai.provider", "openrouter")
	v.SetDefault("ai.model", "moonshotai/kimi-k2.5")
	v.SetDefault("ai.droid.binary_path", "droid")
	v.SetDefault("ai.droid.auto_level", "")
	v.SetDefault("ai.droid.work_dir", "")
	v.SetDefault("auth.mode", "open")
	v.SetDefault("auth.allowed_users", []int64{})
	v.SetDefault("auth.admin_id", int64(0))
	v.SetDefault("groups.default_mode", "standby")
	v.SetDefault("api.enabled", false)
	v.SetDefault("api.port", 8080)
	v.SetDefault("api.bind_addr", "127.0.0.1")
	v.SetDefault("api.api_key", "")
	v.SetDefault("api.webhook_chat", int64(0))
	v.SetDefault("artifacts.roots", []string{})
	v.SetDefault("tts.provider", "openai")
	v.SetDefault("tts.default_voice", "")
	v.SetDefault("stt.base_url", "")
	v.SetDefault("stt.api_key", "")
	v.SetDefault("stt.confidence_threshold", 0.6)
	v.SetDefault("memory.enabled", false)
	v.SetDefault("memory.mode", MemoryModeEager)
	v.SetDefault("memory.backend", "builtin")
	v.SetDefault("memory.embeddings_base_url", "https://api.openai.com/v1")
	v.SetDefault("memory.embeddings_api_key", "")
	v.SetDefault("memory.embeddings_model", "text-embedding-3-small")
	v.SetDefault("memory.extra_paths", []string{})
	v.SetDefault("memory.metadata_extraction", false)
	v.SetDefault("memory.metadata_model", "haiku")
	v.SetDefault("memory.qmd.binary_path", "qmd")
	v.SetDefault("memory.qmd.index", "index")
	v.SetDefault("memory.qmd.index_path", "")
	v.SetDefault("memory.qmd.search_mode", "search")
	v.SetDefault("memory.qmd.timeout", "10s")
	v.SetDefault("memory.qmd.fallback_cooldown", "1m")
	v.SetDefault("memory.qmd.collections.extra_paths", []string{})
	v.SetDefault("memory.mcp.enabled", false)
	v.SetDefault("memory.mcp.host", "127.0.0.1")
	v.SetDefault("memory.mcp.port", 9233)
	v.SetDefault("memory.mcp.endpoint", "/mcp")
	v.SetDefault("memory.mcp.allow_writes", false)
	v.SetDefault("memory.active.enabled", false)
	v.SetDefault("memory.active.timeout_ms", 1500)
	v.SetDefault("memory.active.max_snippets", 5)
	v.SetDefault("memory.active.max_chars", 2000)
	v.SetDefault("memory.active.history_turns", 3)
	v.SetDefault("memory.sessions.enabled", false)
	v.SetDefault("memory.sessions.include_groups", false)
	v.SetDefault("memory.sessions.max_messages_per_session", 0)
	v.SetDefault("memory.curation.enabled", false)
	v.SetDefault("memory.curation.schedule", "")
	v.SetDefault("memory.curation.days", 7)
	v.SetDefault("control.enabled", false)
	v.SetDefault("control.port", 8787)
	v.SetDefault("control.token", "")
	v.SetDefault("control.allow_loopback_without_token", true)
	v.SetDefault("runtime.session_queue_limit", 100)
	v.SetDefault("session.dm_scope", "main")
	v.SetDefault("worktree.base_dir", "")
	v.SetDefault("worktree.stale_age_days", 7)
	v.SetDefault("worktree.worker", "claude")
	v.SetDefault("worktree.model", "")
	v.SetDefault("maestro.repo", "")
	v.SetDefault("maestro.ready_label", "ready")
	v.SetDefault("maestro.hard_exclude_labels", []string{"blocked", "epic", "meta", "question", "wontfix", "duplicate", "invalid"})
	v.SetDefault("maestro.limit", 50)
	v.SetDefault("evolution.enabled", false)
	v.SetDefault("evolution.tasks_per_cycle", 50)
	v.SetDefault("evolution.pass_threshold", 0.8)
	v.SetDefault("evolution.max_diff_percent", 0.2)
	v.SetDefault("evolution.benchmarks_dir", "./benchmarks")
	v.SetDefault("evolution.evolution_dir", "~/.ok-gobot/evolution")

	// Environment variable prefix
	v.SetEnvPrefix("OKGOBOT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Find config directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	configDir := filepath.Join(homeDir, ".ok-gobot")
	configFile := filepath.Join(configDir, "config")

	// Check if config exists
	if _, err := os.Stat(configFile + ".yaml"); err == nil {
		v.SetConfigFile(configFile + ".yaml")
	} else if _, err := os.Stat(configFile + ".yml"); err == nil {
		v.SetConfigFile(configFile + ".yml")
	} else if _, err := os.Stat(configFile + ".json"); err == nil {
		v.SetConfigFile(configFile + ".json")
	}

	// Read config if it exists
	if v.ConfigFileUsed() != "" {
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("failed to read config: %w", err)
		}
	}

	// Unmarshal to struct
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Expand paths
	cfg.StoragePath = expandPath(cfg.StoragePath)
	cfg.SoulPath = expandPath(cfg.SoulPath)
	cfg.RolesPath = expandPath(cfg.RolesPath)
	cfg.Artifacts.Roots = expandPaths(cfg.Artifacts.Roots)
	cfg.Memory.ExtraPaths = expandMemoryExtraPaths(cfg.Memory.ExtraPaths)
	cfg.Evolution.BenchmarksDir = expandPath(cfg.Evolution.BenchmarksDir)
	cfg.Evolution.EvolutionDir = expandPath(cfg.Evolution.EvolutionDir)
	cfg.ConfigPath = v.ConfigFileUsed()

	// Migrate legacy openai config to ai config
	var legacyOpenAI OpenAIConfig
	if err := v.UnmarshalKey("openai", &legacyOpenAI); err == nil {
		if legacyOpenAI.APIKey != "" && cfg.AI.APIKey == "" {
			cfg.AI.APIKey = legacyOpenAI.APIKey
			cfg.AI.Provider = "openai"
			if legacyOpenAI.Model != "" {
				cfg.AI.Model = legacyOpenAI.Model
			}
		}
	}

	return &cfg, nil
}

// LoadFrom reads configuration from a specific file path
func LoadFrom(configPath string) (*Config, error) {
	v := viper.New()

	// Set defaults
	v.SetDefault("log_level", "info")
	v.SetDefault("storage_path", "~/.ok-gobot/ok-gobot.db")
	v.SetDefault("soul_path", bootstrap.DefaultPath)
	v.SetDefault("ai.provider", "openrouter")
	v.SetDefault("ai.model", "moonshotai/kimi-k2.5")
	v.SetDefault("ai.droid.binary_path", "droid")
	v.SetDefault("ai.droid.auto_level", "")
	v.SetDefault("ai.droid.work_dir", "")
	v.SetDefault("auth.mode", "open")
	v.SetDefault("auth.allowed_users", []int64{})
	v.SetDefault("auth.admin_id", int64(0))
	v.SetDefault("groups.default_mode", "standby")
	v.SetDefault("api.enabled", false)
	v.SetDefault("api.port", 8080)
	v.SetDefault("api.bind_addr", "127.0.0.1")
	v.SetDefault("api.api_key", "")
	v.SetDefault("api.webhook_chat", int64(0))
	v.SetDefault("artifacts.roots", []string{})
	v.SetDefault("tts.provider", "openai")
	v.SetDefault("tts.default_voice", "")
	v.SetDefault("stt.base_url", "")
	v.SetDefault("stt.api_key", "")
	v.SetDefault("stt.confidence_threshold", 0.6)
	v.SetDefault("memory.enabled", false)
	v.SetDefault("memory.mode", MemoryModeEager)
	v.SetDefault("memory.backend", "builtin")
	v.SetDefault("memory.embeddings_base_url", "https://api.openai.com/v1")
	v.SetDefault("memory.embeddings_api_key", "")
	v.SetDefault("memory.embeddings_model", "text-embedding-3-small")
	v.SetDefault("memory.extra_paths", []string{})
	v.SetDefault("memory.metadata_extraction", false)
	v.SetDefault("memory.metadata_model", "haiku")
	v.SetDefault("memory.qmd.binary_path", "qmd")
	v.SetDefault("memory.qmd.index", "index")
	v.SetDefault("memory.qmd.index_path", "")
	v.SetDefault("memory.qmd.search_mode", "search")
	v.SetDefault("memory.qmd.timeout", "10s")
	v.SetDefault("memory.qmd.fallback_cooldown", "1m")
	v.SetDefault("memory.qmd.collections.extra_paths", []string{})
	v.SetDefault("memory.mcp.enabled", false)
	v.SetDefault("memory.mcp.host", "127.0.0.1")
	v.SetDefault("memory.mcp.port", 9233)
	v.SetDefault("memory.mcp.endpoint", "/mcp")
	v.SetDefault("memory.mcp.allow_writes", false)
	v.SetDefault("memory.active.enabled", false)
	v.SetDefault("memory.active.timeout_ms", 1500)
	v.SetDefault("memory.active.max_snippets", 5)
	v.SetDefault("memory.active.max_chars", 2000)
	v.SetDefault("memory.active.history_turns", 3)
	v.SetDefault("memory.sessions.enabled", false)
	v.SetDefault("memory.sessions.include_groups", false)
	v.SetDefault("memory.sessions.max_messages_per_session", 0)
	v.SetDefault("memory.curation.enabled", false)
	v.SetDefault("memory.curation.schedule", "")
	v.SetDefault("memory.curation.days", 7)
	v.SetDefault("control.enabled", false)
	v.SetDefault("control.port", 8787)
	v.SetDefault("control.token", "")
	v.SetDefault("control.allow_loopback_without_token", true)
	v.SetDefault("runtime.session_queue_limit", 100)
	v.SetDefault("session.dm_scope", "main")
	v.SetDefault("worktree.base_dir", "")
	v.SetDefault("worktree.stale_age_days", 7)
	v.SetDefault("worktree.worker", "claude")
	v.SetDefault("worktree.model", "")
	v.SetDefault("maestro.repo", "")
	v.SetDefault("maestro.ready_label", "ready")
	v.SetDefault("maestro.hard_exclude_labels", []string{"blocked", "epic", "meta", "question", "wontfix", "duplicate", "invalid"})
	v.SetDefault("maestro.limit", 50)
	v.SetDefault("evolution.enabled", false)
	v.SetDefault("evolution.tasks_per_cycle", 50)
	v.SetDefault("evolution.pass_threshold", 0.8)
	v.SetDefault("evolution.max_diff_percent", 0.2)
	v.SetDefault("evolution.benchmarks_dir", "./benchmarks")
	v.SetDefault("evolution.evolution_dir", "~/.ok-gobot/evolution")

	// Environment variable prefix
	v.SetEnvPrefix("OKGOBOT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Load from specific config file
	v.SetConfigFile(configPath)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	// Unmarshal to struct
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Expand paths
	cfg.StoragePath = expandPath(cfg.StoragePath)
	cfg.SoulPath = expandPath(cfg.SoulPath)
	cfg.RolesPath = expandPath(cfg.RolesPath)
	cfg.Artifacts.Roots = expandPaths(cfg.Artifacts.Roots)
	cfg.Memory.ExtraPaths = expandMemoryExtraPaths(cfg.Memory.ExtraPaths)
	cfg.Evolution.BenchmarksDir = expandPath(cfg.Evolution.BenchmarksDir)
	cfg.Evolution.EvolutionDir = expandPath(cfg.Evolution.EvolutionDir)
	cfg.ConfigPath = configPath

	// Migrate legacy openai config to ai config
	var legacyOpenAI OpenAIConfig
	if err := v.UnmarshalKey("openai", &legacyOpenAI); err == nil {
		if legacyOpenAI.APIKey != "" && cfg.AI.APIKey == "" {
			cfg.AI.APIKey = legacyOpenAI.APIKey
			cfg.AI.Provider = "openai"
			if legacyOpenAI.Model != "" {
				cfg.AI.Model = legacyOpenAI.Model
			}
		}
	}

	return &cfg, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	// Check Telegram token
	if c.Telegram.Token == "" {
		return fmt.Errorf("telegram.token is required")
	}

	// Check AI configuration
	if c.AI.APIKey == "" && c.AI.Provider != "droid" {
		return fmt.Errorf("ai.api_key is required")
	}

	if c.AI.Model == "" {
		return fmt.Errorf("ai.model is required")
	}

	// Validate auth mode
	validModes := map[string]bool{"open": true, "allowlist": true, "pairing": true}
	if !validModes[c.Auth.Mode] {
		return fmt.Errorf("invalid auth.mode: %s (must be 'open', 'allowlist', or 'pairing')", c.Auth.Mode)
	}

	// Validate legacy runtime.mode compatibility.
	validRuntimeModes := map[string]bool{"": true, "hub": true, "legacy": true}
	if !validRuntimeModes[c.Runtime.Mode] {
		return fmt.Errorf("invalid runtime.mode: %q (legacy compatibility only; allowed: '', 'hub', 'legacy')", c.Runtime.Mode)
	}
	if c.Runtime.SessionQueueLimit < 0 {
		return fmt.Errorf("invalid runtime.session_queue_limit: %d (must be >= 0)", c.Runtime.SessionQueueLimit)
	}
	// Validate cost tier names and durations.
	validCostTiers := map[string]bool{
		"premium": true, "standard": true, "cheap": true, "local": true,
	}
	for name, entry := range c.Runtime.CostTiers {
		if !validCostTiers[name] {
			return fmt.Errorf("invalid runtime.cost_tiers key: %q (allowed: premium, standard, cheap, local)", name)
		}
		if entry.MaxDuration != "" {
			if _, err := time.ParseDuration(entry.MaxDuration); err != nil {
				return fmt.Errorf("invalid runtime.cost_tiers.%s.max_duration: %w", name, err)
			}
		}
	}

	// Validate role policies.
	for _, role := range c.Runtime.Roles {
		if role.Name == "" {
			return fmt.Errorf("runtime.roles: each role must have a name")
		}
		if role.DefaultTier != "" && !validCostTiers[role.DefaultTier] {
			return fmt.Errorf("runtime.roles[%s].default_tier: invalid tier %q", role.Name, role.DefaultTier)
		}
		for tierName, entry := range role.Tiers {
			if !validCostTiers[tierName] {
				return fmt.Errorf("runtime.roles[%s].tiers: invalid tier %q", role.Name, tierName)
			}
			if entry.MaxDuration != "" {
				if _, err := time.ParseDuration(entry.MaxDuration); err != nil {
					return fmt.Errorf("runtime.roles[%s].tiers.%s.max_duration: %w", role.Name, tierName, err)
				}
			}
		}
	}

	// Validate session DM scope
	validDMScope := map[string]bool{"main": true, "per_user": true}
	if c.Session.DMScope != "" && !validDMScope[c.Session.DMScope] {
		return fmt.Errorf("invalid session.dm_scope: %s (must be 'main' or 'per_user')", c.Session.DMScope)
	}

	if strings.TrimSpace(c.Maestro.ReadyLabel) == "" {
		return fmt.Errorf("invalid maestro.ready_label: must not be empty")
	}
	if c.Maestro.Limit < 0 {
		return fmt.Errorf("invalid maestro.limit: %d (must be >= 0)", c.Maestro.Limit)
	}

	// Validate memory prompt mode
	if c.Memory.Mode != "" && !IsValidMemoryMode(NormalizeMemoryMode(c.Memory.Mode)) {
		return fmt.Errorf("invalid memory.mode: %q (must be 'eager', 'retrieval_first', or 'startup_recent')", c.Memory.Mode)
	}

	// Validate TTS provider
	if c.TTS.Provider != "" {
		validTTSProviders := map[string]bool{"openai": true, "edge": true}
		if !validTTSProviders[c.TTS.Provider] {
			return fmt.Errorf("invalid tts.provider: %s (must be 'openai' or 'edge')", c.TTS.Provider)
		}
	}

	validMemoryBackends := map[string]bool{"": true, "builtin": true, "qmd": true, "auto": true}
	if !validMemoryBackends[c.Memory.Backend] {
		return fmt.Errorf("invalid memory.backend: %s (must be 'builtin', 'qmd', or 'auto')", c.Memory.Backend)
	}
	validQMDSearchModes := map[string]bool{"": true, "search": true, "vsearch": true, "query": true}
	if !validQMDSearchModes[c.Memory.QMD.SearchMode] {
		return fmt.Errorf("invalid memory.qmd.search_mode: %s (must be 'search', 'vsearch', or 'query')", c.Memory.QMD.SearchMode)
	}
	if c.Memory.QMD.Timeout != "" {
		if _, err := time.ParseDuration(c.Memory.QMD.Timeout); err != nil {
			return fmt.Errorf("invalid memory.qmd.timeout: %w", err)
		}
	}
	if c.Memory.QMD.FallbackCooldown != "" {
		if _, err := time.ParseDuration(c.Memory.QMD.FallbackCooldown); err != nil {
			return fmt.Errorf("invalid memory.qmd.fallback_cooldown: %w", err)
		}
	}

	// Validate agent capability policies.
	validFileWriteScopes := map[string]bool{"": true, "full": true, "read_only": true}
	for _, agent := range c.Agents {
		if agent.Capabilities != nil {
			if !validFileWriteScopes[agent.Capabilities.FileWriteScope] {
				return fmt.Errorf("agents[%s].capabilities.file_write_scope: invalid value %q (must be \"full\" or \"read_only\")", agent.Name, agent.Capabilities.FileWriteScope)
			}
		}
	}

	// Check storage path is set
	if c.StoragePath == "" {
		return fmt.Errorf("storage_path is required")
	}

	return nil
}

// Save writes the current configuration to file
func (c *Config) Save() error {
	if c.ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}

	v := viper.New()
	v.SetConfigFile(c.ConfigPath)

	// Set values
	v.Set("telegram.token", c.Telegram.Token)
	v.Set("telegram.webhook", c.Telegram.Webhook)
	v.Set("ai.provider", c.AI.Provider)
	v.Set("ai.api_key", c.AI.APIKey)
	v.Set("ai.model", c.AI.Model)
	v.Set("ai.base_url", c.AI.BaseURL)
	v.Set("ai.fallback_models", c.AI.FallbackModels)
	v.Set("ai.droid.binary_path", c.AI.Droid.BinaryPath)
	v.Set("ai.droid.auto_level", c.AI.Droid.AutoLevel)
	v.Set("ai.droid.work_dir", c.AI.Droid.WorkDir)
	v.Set("auth.mode", c.Auth.Mode)
	v.Set("auth.allowed_users", c.Auth.AllowedUsers)
	v.Set("auth.admin_id", c.Auth.AdminID)
	v.Set("groups.default_mode", c.Groups.DefaultMode)
	v.Set("api.enabled", c.API.Enabled)
	v.Set("api.port", c.API.Port)
	v.Set("api.bind_addr", c.API.BindAddr)
	v.Set("api.api_key", c.API.APIKey)
	v.Set("api.webhook_chat", c.API.WebhookChat)
	v.Set("artifacts.roots", c.Artifacts.Roots)
	v.Set("tts.provider", c.TTS.Provider)
	v.Set("tts.default_voice", c.TTS.DefaultVoice)
	v.Set("memory.enabled", c.Memory.Enabled)
	v.Set("memory.mode", c.Memory.Mode)
	v.Set("memory.backend", c.Memory.Backend)
	v.Set("memory.embeddings_base_url", c.Memory.EmbeddingsBaseURL)
	v.Set("memory.embeddings_api_key", c.Memory.EmbeddingsAPIKey)
	v.Set("memory.embeddings_model", c.Memory.EmbeddingsModel)
	v.Set("memory.extra_paths", c.Memory.ExtraPaths)
	v.Set("memory.metadata_extraction", c.Memory.MetadataExtraction)
	v.Set("memory.metadata_model", c.Memory.MetadataModel)
	v.Set("memory.qmd.binary_path", c.Memory.QMD.BinaryPath)
	v.Set("memory.qmd.index", c.Memory.QMD.Index)
	v.Set("memory.qmd.index_path", c.Memory.QMD.IndexPath)
	v.Set("memory.qmd.search_mode", c.Memory.QMD.SearchMode)
	v.Set("memory.qmd.timeout", c.Memory.QMD.Timeout)
	v.Set("memory.qmd.fallback_cooldown", c.Memory.QMD.FallbackCooldown)
	v.Set("memory.qmd.collections.workspace", c.Memory.QMD.Collections.Workspace)
	v.Set("memory.qmd.collections.daily_notes", c.Memory.QMD.Collections.DailyNotes)
	v.Set("memory.qmd.collections.session_transcripts", c.Memory.QMD.Collections.SessionTranscripts)
	v.Set("memory.qmd.collections.extra_paths", c.Memory.QMD.Collections.ExtraPaths)
	v.Set("memory.mcp.enabled", c.Memory.MCP.Enabled)
	v.Set("memory.mcp.host", c.Memory.MCP.Host)
	v.Set("memory.mcp.port", c.Memory.MCP.Port)
	v.Set("memory.mcp.endpoint", c.Memory.MCP.Endpoint)
	v.Set("memory.mcp.allow_writes", c.Memory.MCP.AllowWrites)
	v.Set("memory.active.enabled", c.Memory.Active.Enabled)
	v.Set("memory.active.timeout_ms", c.Memory.Active.TimeoutMS)
	v.Set("memory.active.max_snippets", c.Memory.Active.MaxSnippets)
	v.Set("memory.active.max_chars", c.Memory.Active.MaxChars)
	v.Set("memory.active.history_turns", c.Memory.Active.HistoryTurns)
	v.Set("memory.sessions.enabled", c.Memory.Sessions.Enabled)
	v.Set("memory.sessions.include_groups", c.Memory.Sessions.IncludeGroups)
	v.Set("memory.sessions.max_messages_per_session", c.Memory.Sessions.MaxMessagesPerSession)
	v.Set("memory.curation.enabled", c.Memory.Curation.Enabled)
	v.Set("memory.curation.schedule", c.Memory.Curation.Schedule)
	v.Set("memory.curation.days", c.Memory.Curation.Days)
	v.Set("storage_path", c.StoragePath)
	v.Set("soul_path", c.SoulPath)
	v.Set("roles_path", c.RolesPath)
	v.Set("log_level", c.LogLevel)
	v.Set("runtime.session_queue_limit", c.Runtime.SessionQueueLimit)
	if len(c.Runtime.CostTiers) > 0 {
		v.Set("runtime.cost_tiers", c.Runtime.CostTiers)
	}
	if len(c.Runtime.Roles) > 0 {
		v.Set("runtime.roles", c.Runtime.Roles)
	}
	v.Set("session.dm_scope", c.Session.DMScope)

	// Persist fields that were previously omitted causing lossy round-trips.
	if len(c.Models) > 0 {
		v.Set("models", c.Models)
	}
	if len(c.ModelAliases) > 0 {
		v.Set("model_aliases", c.ModelAliases)
	}
	if len(c.Contacts) > 0 {
		v.Set("contacts", c.Contacts)
	}
	v.Set("control.enabled", c.Control.Enabled)
	v.Set("control.port", c.Control.Port)
	v.Set("control.token", c.Control.Token)
	v.Set("control.allow_loopback_without_token", c.Control.AllowLoopbackWithoutToken)
	if len(c.Agents) > 0 {
		v.Set("agents", c.Agents)
	}

	return v.WriteConfig()
}

// GetSoulPath returns the soul path, checking env var first
func (c *Config) GetSoulPath() string {
	// Check environment variable first
	if envPath := os.Getenv("OKGOBOT_SOUL_PATH"); envPath != "" {
		return expandPath(envPath)
	}
	return c.SoulPath
}

// expandPath expands ~ to home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(homeDir, path[2:])
	}
	return path
}

func expandPaths(paths []string) []string {
	if paths == nil {
		return nil
	}
	out := make([]string, len(paths))
	for i, path := range paths {
		out[i] = expandPath(path)
	}
	return out
}

func expandMemoryExtraPaths(paths []MemoryExtraPathConfig) []MemoryExtraPathConfig {
	if len(paths) == 0 {
		return nil
	}
	out := make([]MemoryExtraPathConfig, len(paths))
	copy(out, paths)
	for i := range out {
		out[i].Path = expandPath(out[i].Path)
	}
	return out
}
