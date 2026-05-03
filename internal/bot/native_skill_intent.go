package bot

import (
	"regexp"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/karaoke"
	"ok-gobot/internal/videosummary"
)

var urlLikeRE = regexp.MustCompile(`https?://[^\s<>()]+`)

type nativeSkillIntent struct {
	Skill string
	URL   string
}

func (b *Bot) handleNativeSkillIntent(c telebot.Context, content string) (bool, error) {
	intent, ok := detectNativeSkillIntent(content)
	if !ok {
		return false, nil
	}

	msg := c.Message()
	originalPayload := msg.Payload
	msg.Payload = intent.URL
	defer func() { msg.Payload = originalPayload }()

	switch intent.Skill {
	case "video-summary":
		return true, b.handleVideoSummaryCommand(c)
	case "karaoke":
		return true, b.handleKaraokeCommand(c)
	default:
		return false, nil
	}
}

func detectNativeSkillIntent(content string) (nativeSkillIntent, bool) {
	rawURL, ok := singleYouTubeURL(content)
	if !ok {
		return nativeSkillIntent{}, false
	}

	lower := strings.ToLower(content)
	karaokeHit := containsAny(lower, []string{
		"karaoke",
		"караоке",
		"минус",
		"минусов",
		"убери голос",
		"убрать голос",
		"убери вокал",
		"убрать вокал",
		"без вокала",
		"instrumental",
		"no vocals",
		"vocals stem",
		"separate vocals",
	})
	videoSummaryHit := containsAny(lower, []string{
		"video_summary",
		"video-summary",
		"summary",
		"summarize",
		"digest",
		"transcript",
		"transcribe",
		"конспект",
		"саммари",
		"суммар",
		"резюме",
		"краткое содержание",
		"транскрип",
		"расшифров",
		"пересказ",
		"тезисы",
	})

	switch {
	case karaokeHit && !videoSummaryHit:
		return nativeSkillIntent{Skill: "karaoke", URL: rawURL}, true
	case videoSummaryHit && !karaokeHit:
		return nativeSkillIntent{Skill: "video-summary", URL: rawURL}, true
	default:
		return nativeSkillIntent{}, false
	}
}

func singleYouTubeURL(content string) (string, bool) {
	matches := urlLikeRE.FindAllString(content, -1)
	seen := make(map[string]struct{})
	var found string
	for _, match := range matches {
		candidate := strings.TrimRight(match, ".,;:!?)]}")
		if _, exists := seen[candidate]; exists {
			continue
		}
		if videosummary.ValidateYouTubeURL(candidate) != nil && karaoke.ValidateYouTubeURL(candidate) != nil {
			continue
		}
		seen[candidate] = struct{}{}
		if found != "" {
			return "", false
		}
		found = candidate
	}
	return found, found != ""
}

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
