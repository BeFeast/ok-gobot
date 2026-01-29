package bot

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	replyCurrentTag = "[[reply_to_current]]"
	replyToPattern  = regexp.MustCompile(`\[\[reply_to:(\d+)\]\]`)
)

// ReplyTarget holds parsed reply information
type ReplyTarget struct {
	MessageID int    // 0 means no reply, -1 means reply to current
	Clean     string // message with tags stripped
}

// parseReplyTags extracts reply tags from text and returns clean text with target info
func parseReplyTags(text string) ReplyTarget {
	result := ReplyTarget{Clean: text}

	if strings.Contains(text, replyCurrentTag) {
		result.MessageID = -1
		result.Clean = strings.ReplaceAll(text, replyCurrentTag, "")
		result.Clean = strings.TrimSpace(result.Clean)
		return result
	}

	if match := replyToPattern.FindStringSubmatch(text); len(match) == 2 {
		if id, err := strconv.Atoi(match[1]); err == nil {
			result.MessageID = id
		}
		result.Clean = replyToPattern.ReplaceAllString(text, "")
		result.Clean = strings.TrimSpace(result.Clean)
	}

	return result
}
