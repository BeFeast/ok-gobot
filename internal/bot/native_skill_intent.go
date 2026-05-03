package bot

import (
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/karaoke"
	"ok-gobot/internal/videosummary"
)

var urlLikeRE = regexp.MustCompile(`https?://[^\s<>()]+`)

type nativeSkillIntent struct {
	Skill      string
	URL        string
	InvalidURL bool
}

func (b *Bot) handleNativeSkillIntent(c telebot.Context, content string) (bool, error) {
	intent, ok := detectNativeSkillIntent(content)
	if !ok {
		return false, nil
	}
	if intent.InvalidURL {
		switch intent.Skill {
		case "video-summary":
			return true, c.Send("Usage: /video_summary <youtube_url>")
		case "karaoke":
			return true, c.Send("Usage: /karaoke <youtube_url>")
		default:
			return false, nil
		}
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

	var skill string
	switch {
	case karaokeHit && !videoSummaryHit:
		skill = "karaoke"
	case videoSummaryHit && !karaokeHit:
		skill = "video-summary"
	default:
		return nativeSkillIntent{}, false
	}

	if rawURL, ok := singleYouTubeURL(content); ok {
		return nativeSkillIntent{Skill: skill, URL: rawURL}, true
	}
	if rawURL, ok := singleInvalidYouTubeURL(content); ok {
		return nativeSkillIntent{Skill: skill, URL: rawURL, InvalidURL: true}, true
	}
	return nativeSkillIntent{}, false
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

func singleInvalidYouTubeURL(content string) (string, bool) {
	matches := urlLikeRE.FindAllString(content, -1)
	seen := make(map[string]struct{})
	var found string
	for _, match := range matches {
		candidate := strings.TrimRight(match, ".,;:!?)]}")
		if _, exists := seen[candidate]; exists {
			continue
		}
		if !isYouTubeHost(candidate) {
			continue
		}
		seen[candidate] = struct{}{}
		if videosummary.ValidateYouTubeURL(candidate) == nil || karaoke.ValidateYouTubeURL(candidate) == nil {
			return "", false
		}
		if found != "" {
			return "", false
		}
		found = candidate
	}
	return found, found != ""
}

func isYouTubeHost(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "youtu.be" || host == "youtube.com" || host == "www.youtube.com" || strings.HasSuffix(host, ".youtube.com")
}

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
