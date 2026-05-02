package bot

import (
	"regexp"
	"strings"
)

var (
	telegramInlineCodeRE = regexp.MustCompile("`([^`\n]+)`")
	telegramBulletRE     = regexp.MustCompile(`(?m)^[ \t]*\*[ \t]+`)
	telegramHeadingRE    = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+`)
	genericOfferTailRE   = regexp.MustCompile(`(?is)\n+\s*(Вам нужно что-то конкретное.*|Если хотите,.*|Чем могу помочь.*|Есть ли что-то конкретное.*)\s*$`)
)

func sanitizeTelegramModelReply(text string) string {
	text = telegramHeadingRE.ReplaceAllString(text, "")
	text = telegramBulletRE.ReplaceAllString(text, "• ")
	text = telegramInlineCodeRE.ReplaceAllStringFunc(text, func(match string) string {
		return strings.Trim(match, "`")
	})
	text = strings.ReplaceAll(text, "**", "")
	text = genericOfferTailRE.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}
