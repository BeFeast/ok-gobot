package runtime

import (
	"regexp"
	"strings"
)

// ChatAction is the rules-first action chosen for an inbound chat turn.
type ChatAction string

const (
	// ChatActionReply keeps the message on the fast inline reply path.
	ChatActionReply ChatAction = "reply"
	// ChatActionClarify asks the user for a missing detail before doing work.
	ChatActionClarify ChatAction = "clarification"
	// ChatActionLaunchJob moves the work onto an isolated background job path.
	ChatActionLaunchJob ChatAction = "launch_job"
)

// ChatRouteDecision is the output of the rules-first chat router.
type ChatRouteDecision struct {
	Action        ChatAction
	Reason        string
	Clarification string
}

var (
	ambiguousTaskPatterns = []*regexp.Regexp{
		regexp.MustCompile(`^(please )?(task|job|background job|background task)[.!?]*$`),
		regexp.MustCompile(`^(please )?(help|start|continue|go ahead)[.!?]*$`),
		regexp.MustCompile(`^(please )?(do|handle|work on|check|review|look into|investigate|analy[sz]e|debug|fix|run|build|implement|finish|summari[sz]e|compare)( (it|this|that|these|those))?[.!?]*$`),
		regexp.MustCompile(`^(please )?(can|could) you (do|handle|work on|check|review|look into|investigate|analy[sz]e|debug|fix|run|build|implement|finish|summari[sz]e|compare)( (it|this|that|these|those))?[?!.]*$`),
	}
	heavyLeadPhrases = []string{
		"investigate ",
		"look into ",
		"debug ",
		"fix ",
		"implement ",
		"refactor ",
		"review ",
		"browse ",
		"scrape ",
		"run ",
		"test ",
		"build ",
		"open a pr",
		"create a pr",
		"prepare a pr",
	}
	heavyContextTerms = []string{
		"repo",
		"repository",
		"codebase",
		"code",
		"file",
		"files",
		"branch",
		"commit",
		"pr",
		"pull request",
		"issue",
		"bug",
		"failing test",
		"tests",
		"log",
		"logs",
		"stack trace",
		"website",
		"site",
		"browser",
		"page",
		"pages",
		"doc",
		"docs",
		"documentation",
		"server",
		"service",
		"services",
		"api",
		"apis",
	}
	forcedJobPrefixes = []string{"job:", "task:", "background:"}
)

const defaultClarification = "What should I work on exactly? Include the repo, file, site, or desired outcome."

// DecideChatRoute applies deterministic rules to keep lightweight turns on the
// reply path while moving obvious work requests onto an explicit job path.
func DecideChatRoute(input string) ChatRouteDecision {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return ChatRouteDecision{
			Action:        ChatActionClarify,
			Reason:        "empty_request",
			Clarification: defaultClarification,
		}
	}

	normalized := normalizeChatInput(raw)
	if prefix, desc, ok := splitForcedJobPrefix(raw, normalized); ok {
		descNorm := normalizeChatInput(desc)
		if descNorm == "" || isAmbiguousTaskRequest(descNorm) {
			return ChatRouteDecision{
				Action:        ChatActionClarify,
				Reason:        "forced_job_missing_scope",
				Clarification: defaultClarification,
			}
		}
		return ChatRouteDecision{
			Action: ChatActionLaunchJob,
			Reason: "forced_job_prefix:" + prefix,
		}
	}

	if isAmbiguousTaskRequest(normalized) {
		return ChatRouteDecision{
			Action:        ChatActionClarify,
			Reason:        "ambiguous_task_request",
			Clarification: defaultClarification,
		}
	}

	if isHeavyJobRequest(raw, normalized) {
		return ChatRouteDecision{
			Action: ChatActionLaunchJob,
			Reason: "heavy_work_request",
		}
	}

	return ChatRouteDecision{
		Action: ChatActionReply,
		Reason: "default_reply",
	}
}

func normalizeChatInput(input string) string {
	return strings.ToLower(strings.Join(strings.Fields(input), " "))
}

func splitForcedJobPrefix(raw, normalized string) (prefix, desc string, ok bool) {
	for _, candidate := range forcedJobPrefixes {
		if !strings.HasPrefix(normalized, candidate) {
			continue
		}
		idx := strings.Index(raw, ":")
		if idx == -1 {
			return candidate, "", true
		}
		return candidate, strings.TrimSpace(raw[idx+1:]), true
	}
	return "", "", false
}

func isAmbiguousTaskRequest(normalized string) bool {
	for _, pattern := range ambiguousTaskPatterns {
		if pattern.MatchString(normalized) {
			return true
		}
	}
	return false
}

func isHeavyJobRequest(raw, normalized string) bool {
	lead := countContains(normalized, heavyLeadPhrases)
	context := countContains(normalized, heavyContextTerms)

	score := 0
	if lead > 0 {
		score += 2
	}
	if context > 0 {
		score++
	}
	if context > 1 {
		score++
	}
	if strings.Contains(raw, "\n") {
		score++
	}
	if strings.Contains(raw, "```") {
		score += 2
	}
	if len(raw) >= 180 {
		score++
	}

	return score >= 3
}

func countContains(input string, needles []string) int {
	hits := 0
	for _, needle := range needles {
		if strings.Contains(input, needle) {
			hits++
		}
	}
	return hits
}
