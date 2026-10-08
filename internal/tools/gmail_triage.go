package tools

import (
	"context"
	"fmt"
	"strings"
)

// GmailTriageController serves gmail_triage requests for the mailbox profile
// bound to a chat. The bot implements it.
type GmailTriageController interface {
	GmailTriageToolCommand(ctx context.Context, chatID int64, params map[string]string) (string, error)
}

// GmailTriageTool lets the agent run the email digest and manage its
// schedule and rules. It has no operation that reads message content into
// the conversation or changes mail: trash and send exist only as digest
// buttons the user presses.
type GmailTriageTool struct {
	controller GmailTriageController
	chatID     int64
}

// NewGmailTriageTool creates the unbound tool; the resolver binds it per chat.
func NewGmailTriageTool(controller GmailTriageController) *GmailTriageTool {
	return &GmailTriageTool{controller: controller}
}

func (t *GmailTriageTool) Name() string { return "gmail_triage" }

func (t *GmailTriageTool) Description() string {
	return "Email triage digest for this chat's mailbox: run it now, show status, change the digest schedule (times and weekdays), pause or resume scheduled digests, and manage sorting rules by sender or domain. Use it when the user asks about their email digest, its timing, or how certain mail should be sorted (e.g. 'anything from Bandcamp is ignore'). It never sends, deletes or reads out mail."
}

// BindChat implements ChatScoped.
func (t *GmailTriageTool) BindChat(_ MediaSender, chatID int64) Tool {
	return &GmailTriageTool{controller: t.controller, chatID: chatID}
}

func (t *GmailTriageTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"action": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"run", "status", "schedule", "pause", "resume", "rules"},
				"description": "run: send a digest now. status: mode, schedule, pause state, rule count. schedule: show/set/reset digest times. pause/resume: stop or restart scheduled digests (run still works). rules: list/add/remove sorting rules.",
			},
			"op": map[string]interface{}{
				"type":        "string",
				"description": "For schedule: show | set | reset. For rules: list | add | remove.",
			},
			"times": map[string]interface{}{
				"type":        "string",
				"description": "schedule set: comma-separated 24h local times, e.g. \"10:00,19:00\". Omit to keep current times.",
			},
			"days": map[string]interface{}{
				"type":        "string",
				"description": "schedule set: weekdays the digest runs, e.g. \"sun-thu\", \"mon-fri\", \"mon,wed,fri\". Omit to keep the current days; pass \"\" for every day. To skip weekends, list the remaining days explicitly.",
			},
			"timezone": map[string]interface{}{
				"type":        "string",
				"description": "schedule set: IANA timezone; omit to keep the current one.",
			},
			"scope": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"sender", "domain", "local"},
				"description": "rules add: sender = one address; domain = a whole domain (subdomains included); local = the part before @ on any domain (\"from root\" = root@*). Inferred from value when omitted.",
			},
			"value": map[string]interface{}{
				"type":        "string",
				"description": "rules add: sender address (name@example.com), domain (example.com) or local part on any domain (root@*).",
			},
			"bucket": map[string]interface{}{
				"type":        "string",
				"description": "rules add: target bucket key. Owner mailboxes: reply, action, meetings, fyi, bulk. Sales inbox: sales, urgent, needs_reply, ignore.",
			},
			"note": map[string]interface{}{
				"type":        "string",
				"description": "rules add: the user's own words, kept as context for the classifier.",
			},
			"id": map[string]interface{}{
				"type":        "string",
				"description": "rules remove: rule id from rules list.",
			},
		},
		"required": []string{"action"},
	}
}

func (t *GmailTriageTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	if t.controller == nil {
		return "", fmt.Errorf("gmail triage is not configured")
	}
	if t.chatID == 0 {
		return "", fmt.Errorf("gmail_triage needs a chat context")
	}
	return t.controller.GmailTriageToolCommand(ctx, t.chatID, params)
}

// Execute accepts "action [op] [key=value ...]" for positional callers.
func (t *GmailTriageTool) Execute(ctx context.Context, args ...string) (string, error) {
	params := map[string]string{}
	for i, a := range args {
		if k, v, ok := strings.Cut(a, "="); ok {
			params[k] = v
			continue
		}
		switch i {
		case 0:
			params["action"] = a
		case 1:
			params["op"] = a
		}
	}
	return t.ExecuteJSON(ctx, params)
}
