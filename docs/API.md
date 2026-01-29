# ok-gobot API Reference

Internal Go API reference for ok-gobot packages.

## Package: ai

### Types

```go
type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type StreamChunk struct {
    Content      string
    Done         bool
    FinishReason string
    Error        error
}

type ProviderConfig struct {
    Name    string
    APIKey  string
    BaseURL string
    Model   string
}
```

### Interfaces

```go
type Client interface {
    Complete(ctx context.Context, messages []Message) (string, error)
}

type StreamingClient interface {
    Client
    CompleteStream(ctx context.Context, messages []Message) <-chan StreamChunk
}
```

### Functions

```go
// NewClient creates a new AI client
func NewClient(config ProviderConfig) (*OpenAICompatibleClient, error)

// AvailableModels returns common models for each provider
func AvailableModels() map[string][]string
```

---

## Package: agent

### Personality

```go
type Personality struct {
    // private fields
}

func NewPersonality(basePath string) (*Personality, error)
func (p *Personality) GetSystemPrompt() string
func (p *Personality) GetName() string
func (p *Personality) GetEmoji() string
func (p *Personality) GetFileContent(filename string) (string, bool)
```

### Memory

```go
type Memory struct {
    BasePath string
}

type DailyNote struct {
    Date    string
    Content string
    Path    string
}

func NewMemory(basePath string) *Memory
func (m *Memory) GetTodayNote() (*DailyNote, error)
func (m *Memory) GetNote(date string) (*DailyNote, error)
func (m *Memory) AppendToToday(content string) error
func (m *Memory) LoadLongTermMemory() (string, error)
func (m *Memory) GetRecentContext(days int) (string, error)
```

### TokenCounter

```go
type TokenCounter struct {}

func NewTokenCounter() *TokenCounter
func (tc *TokenCounter) CountTokens(text string) int
func (tc *TokenCounter) CountMessages(messages []Message) int
func (tc *TokenCounter) ShouldCompact(messages []Message, model string, threshold float64) bool

func ModelLimits(model string) int
```

### Compactor

```go
type Compactor struct {
    // private fields
}

type CompactionResult struct {
    Summary        string
    OriginalTokens int
    SummaryTokens  int
    TokensSaved    int
}

func NewCompactor(aiClient ai.Client, model string) *Compactor
func (c *Compactor) SetThreshold(threshold float64)
func (c *Compactor) ShouldCompact(messages []ai.Message) bool
func (c *Compactor) Compact(ctx context.Context, messages []ai.Message) (*CompactionResult, error)
```

### ToolCallingAgent

```go
type ToolCallingAgent struct {
    // private fields
}

type AgentResponse struct {
    Message    string
    ToolUsed   bool
    ToolName   string
    ToolResult string
}

func NewToolCallingAgent(aiClient ai.Client, toolRegistry *tools.Registry, personality *Personality) *ToolCallingAgent
func (a *ToolCallingAgent) ProcessRequest(ctx context.Context, userMessage string, session string) (*AgentResponse, error)
func (a *ToolCallingAgent) GetAvailableTools() []string
```

### Heartbeat

```go
type Heartbeat struct {
    BasePath string
    State    *session.HeartbeatState
}

type HeartbeatResult struct {
    Timestamp      time.Time
    Checks         map[string]CheckResult
    ContextWarning string
    Emails         []EmailInfo
}

type CheckResult struct {
    Status  string // ok, warning, info, error
    Message string
}

type IMAPConfig struct {
    Server   string
    Port     int
    Username string
    Password string
    UseTLS   bool
}

func NewHeartbeat(basePath string) (*Heartbeat, error)
func (h *Heartbeat) Check(ctx context.Context) (*HeartbeatResult, error)
func (h *Heartbeat) ConfigureIMAP(cfg *IMAPConfig)
func (h *Heartbeat) RegisterChecker(name string, checker HeartbeatChecker)
```

---

## Package: bot

### Bot

```go
type Bot struct {
    // private fields
}

type AIConfig struct {
    Provider string
    Model    string
    APIKey   string
}

func New(token string, store *storage.Store, aiClient ai.Client, aiCfg AIConfig, personality *agent.Personality) (*Bot, error)
func (b *Bot) Start(ctx context.Context) error
func (b *Bot) EnableStreaming(enable bool)
```

### StreamEditor

```go
type StreamEditor struct {
    // private fields
}

func NewStreamEditor(bot *telebot.Bot, chat *telebot.Chat, initialMsg *telebot.Message) *StreamEditor
func (e *StreamEditor) Append(text string)
func (e *StreamEditor) Finish() string
func (e *StreamEditor) GetContent() string
```

### MediaHandler

```go
type MediaHandler struct {
    // private fields
}

func NewMediaHandler(bot *telebot.Bot) *MediaHandler
func (m *MediaHandler) HandlePhoto(c telebot.Context) (filePath, caption string, err error)
func (m *MediaHandler) HandleVoice(c telebot.Context) (filePath, transcription string, err error)
func (m *MediaHandler) HandleAudio(c telebot.Context) (filePath, transcription string, err error)
func (m *MediaHandler) HandleDocument(c telebot.Context) (filePath, content string, err error)
func (m *MediaHandler) SendPhoto(chat *telebot.Chat, photoPath, caption string) error
func (m *MediaHandler) SendDocument(chat *telebot.Chat, docPath, caption string) error
func (m *MediaHandler) SendVoice(chat *telebot.Chat, voicePath string) error
```

---

## Package: storage

### Store

```go
type Store struct {
    // private fields
}

type SessionMessage struct {
    ID        int64
    SessionID int64
    ChatID    int64
    Role      string
    Content   string
    CreatedAt string
}

type CronJob struct {
    ID         int64
    Expression string
    Task       string
    ChatID     int64
    NextRun    string
    Enabled    bool
    CreatedAt  string
}

func New(dbPath string) (*Store, error)
func (s *Store) Close() error

// Messages
func (s *Store) SaveMessage(chatID, messageID, userID int64, username, content string) error

// Sessions
func (s *Store) GetSession(chatID int64) (string, error)
func (s *Store) SaveSession(chatID int64, state string) error
func (s *Store) SaveSessionMessage(chatID int64, role, content string) error
func (s *Store) GetSessionMessages(chatID int64, limit int) ([]SessionMessage, error)
func (s *Store) ListSessions(limit int) ([]map[string]interface{}, error)
func (s *Store) SaveSessionSummary(chatID int64, summary string) error

// Cron
func (s *Store) SaveCronJob(expression, task string, chatID int64) (int64, error)
func (s *Store) GetCronJobs() ([]CronJob, error)
func (s *Store) DeleteCronJob(id int64) error
func (s *Store) ToggleCronJob(id int64, enabled bool) error
```

---

## Package: cron

### Scheduler

```go
type JobExecutor func(ctx context.Context, job storage.CronJob) error

type Scheduler struct {
    // private fields
}

func NewScheduler(store *storage.Store, executor JobExecutor) *Scheduler
func (s *Scheduler) Start(ctx context.Context) error
func (s *Scheduler) Stop()
func (s *Scheduler) AddJob(expression, task string, chatID int64) (int64, error)
func (s *Scheduler) RemoveJob(jobID int64) error
func (s *Scheduler) ToggleJob(jobID int64, enabled bool) error
func (s *Scheduler) ListJobs() ([]storage.CronJob, error)
func (s *Scheduler) GetNextRun(jobID int64) (time.Time, error)
```

---

## Package: tools

### Tool Interface

```go
type Tool interface {
    Name() string
    Description() string
    Execute(ctx context.Context, args ...string) (string, error)
}
```

### Registry

```go
type Registry struct {
    // private fields
}

func NewRegistry() *Registry
func (r *Registry) Register(tool Tool)
func (r *Registry) Get(name string) (Tool, bool)
func (r *Registry) List() []Tool
func (r *Registry) Execute(ctx context.Context, toolName string, args ...string) (string, error)

func LoadFromConfig(basePath string) (*Registry, error)
```

### Available Tools

| Tool | Constructor |
|------|-------------|
| LocalCommand | `&LocalCommand{}` |
| SSHTool | `NewSSHTool(host, user)` |
| FileTool | `&FileTool{BasePath: path}` |
| ObsidianTool | `NewObsidianTool(vaultPath)` |
| BrowserTool | `NewBrowserTool(profilePath)` |
| SearchTool | `NewSearchTool(apiKey, engine)` |
| WebFetchTool | `NewWebFetchTool()` |
| MessageTool | `NewMessageTool(sender)` |
| CronTool | `NewCronTool(scheduler, chatID)` |
| ImageTool | `NewImageTool(apiKey, baseURL)` |
| TTSTool | `NewTTSTool(apiKey, baseURL)` |
