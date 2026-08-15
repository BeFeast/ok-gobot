package bot

// Single home for the user-visible chat-status vocabulary. Internal job ids
// stay in /jobs and logs; chat messages carry human phrases only. This file
// is the seed of the personality-owned message catalog (Forgejo issue #8).

// telegramStatusPhrase renders a lifecycle state as a short human phrase.
func telegramStatusPhrase(status telegramJobStatus) string {
	switch status {
	case jobStatusAccepted:
		return "💭 On it…"
	case jobStatusQueued:
		return "⏳ Queued"
	case jobStatusRunning:
		return "💭 Working…"
	case jobStatusCompleted:
		return "✅ Done"
	case jobStatusFailed:
		return "❌ Something went wrong"
	case jobStatusCancelled:
		return "🛑 Stopped"
	default:
		return "💭 On it…"
	}
}

// Lifecycle detail lines and route-level ack texts. Everything the chat
// pipeline says to the user lives in this file.
const (
	queuedAckDetail         = "I’ll get to this right after the current task."
	interruptedDetail       = "Switching to your newer message."
	stoppedBeforeDoneDetail = "The run was stopped before completion."
	silentReplyDetail       = "Completed with no direct reply."
	genericFailureDetail    = "Sorry, I encountered an error processing your request."
	runFailureText          = "❌ Sorry, I encountered an error processing your request."
)

// backgroundJobAck renders the ack for a router-launched background job.
func backgroundJobAck(task string) string {
	return "🚀 Working on it in the background:\n" + task
}

// failedStatusDetail appends a short operator reference to failure details
// so logs can be correlated; happy paths never carry internal ids.
func failedStatusDetail(detail, jobID string) string {
	if jobID == "" {
		return detail
	}
	if detail != "" {
		detail += "\n"
	}
	return detail + "ref: " + jobID
}

// spinnerFrames animate the running header between content updates.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// runningHeader renders the animated running state for the live placeholder.
func runningHeader(frame int) string {
	return spinnerFrames[frame%len(spinnerFrames)] + " Working…"
}

// toolStatusPhrases translate internal tool names into human activities.
// Unknown tools fall back to a gear icon with the raw name.
var toolStatusPhrases = map[string]string{
	"memory_search":   "🔎 searching memory",
	"memory_get":      "📖 reading memory",
	"search":          "🔎 searching the web",
	"web_fetch":       "🌐 fetching a page",
	"browser":         "🌐 browsing",
	"browser_task":    "🌐 browsing",
	"image_gen":       "🎨 painting an image",
	"local":           "🖥️ running a command",
	"ssh":             "🖥️ running a remote command",
	"file":            "📄 working with files",
	"patch":           "📝 editing files",
	"grep":            "🗂️ searching files",
	"obsidian":        "📓 checking the vault",
	"cron":            "⏰ scheduling",
	"message":         "✉️ sending a message",
	"tts":             "🔊 preparing audio",
	"session_search":  "🧵 recalling the conversation",
	"session_get":     "🧵 rereading the conversation",
	"frontend_verify": "🖼️ verifying the UI",
	"recommend_roles": "🧭 thinking about roles",
}

func toolStatusPhrase(name string) string {
	if phrase, ok := toolStatusPhrases[name]; ok {
		return phrase
	}
	return "⚙️ " + name
}
