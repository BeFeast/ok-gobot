package gmailtriage

import (
	"strings"
)

var noReplyLocalParts = []string{"noreply", "no-reply", "no_reply", "donotreply", "do-not-reply", "do_not_reply", "notification", "notifications", "mailer-daemon", "postmaster", "bounce", "bounces"}

var bulkCategories = map[string]bool{
	"CATEGORY_PROMOTIONS": true,
	"CATEGORY_UPDATES":    true,
	"CATEGORY_SOCIAL":     true,
	"CATEGORY_FORUMS":     true,
}

// prefilter decides obvious automated mail without the LLM. It returns the
// quiet bucket and a one-line reason, or ok=false when the thread needs the
// classifier. A thread the owner has written in is never prefiltered.
//
// The sales lens is narrower: contact forms often arrive from noreply senders
// or in Updates, and a missed buyer costs more than an extra line, so only
// marketing signals (promotions, list headers) skip the classifier there.
func prefilter(p Profile, t Thread) (bucket, why string, ok bool) {
	if len(t.Messages) == 0 {
		return "", "", false
	}
	for _, m := range t.Messages {
		if m.HasLabel("SENT") || m.FromAddress() == p.Account {
			return "", "", false
		}
	}
	last := t.Last()
	reason := ""
	switch {
	case last.HasLabel("CATEGORY_PROMOTIONS"):
		reason = tr(p.Language, "Gmail Promotions", "Gmail Promotions")
	case strings.TrimSpace(last.ListUnsubscribe) != "":
		reason = tr(p.Language, "mailing list (List-Unsubscribe)", "рассылка (List-Unsubscribe)")
	case strings.TrimSpace(last.ListID) != "":
		reason = tr(p.Language, "mailing list (List-Id)", "рассылка (List-Id)")
	case isBulkPrecedence(last.Precedence):
		reason = tr(p.Language, "bulk mail (Precedence)", "массовая рассылка (Precedence)")
	}
	if reason == "" && p.Taxonomy == TaxonomyOwner {
		switch {
		case isAutoSubmitted(last.AutoSubmitted):
			reason = tr(p.Language, "automatic message (Auto-Submitted)", "автоматическое письмо (Auto-Submitted)")
		case isNoReply(last.FromAddress()):
			reason = tr(p.Language, "notification from a no-reply address", "уведомление с no-reply адреса")
		default:
			for _, l := range last.Labels {
				if bulkCategories[l] {
					reason = tr(p.Language, "Gmail category ", "категория Gmail ") + strings.TrimPrefix(l, "CATEGORY_")
					break
				}
			}
		}
	}
	if reason == "" {
		return "", "", false
	}
	return p.Taxonomy.QuietBucket(), reason, true
}

func isNoReply(addr string) bool {
	local, _, ok := strings.Cut(addr, "@")
	if !ok {
		return false
	}
	local = strings.ToLower(local)
	for _, p := range noReplyLocalParts {
		if local == p || strings.HasPrefix(local, p+"+") || strings.HasPrefix(local, p+".") || strings.HasPrefix(local, p+"-") {
			return true
		}
	}
	return false
}

func isBulkPrecedence(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "bulk", "list", "junk":
		return true
	}
	return false
}

func isAutoSubmitted(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v != "" && v != "no"
}

// freemailDomains are shared by unrelated people, so domain rules are refused.
var freemailDomains = map[string]bool{
	"gmail.com": true, "googlemail.com": true, "yahoo.com": true, "outlook.com": true, "hotmail.com": true,
	"live.com": true, "icloud.com": true, "me.com": true, "mac.com": true, "aol.com": true, "proton.me": true,
	"protonmail.com": true, "gmx.com": true, "gmx.de": true, "mail.ru": true, "yandex.ru": true, "ya.ru": true,
	"walla.co.il": true, "zoho.com": true,
}

// IsFreemail reports whether a domain is a public mailbox provider.
func IsFreemail(domain string) bool { return freemailDomains[strings.ToLower(domain)] }

// matchRule returns the most specific rule for a sender: the address, then
// the local part on any domain (root@*), then the domain.
func matchRule(rules []Rule, sender string) (Rule, bool) {
	sender = strings.ToLower(sender)
	local, domain, _ := strings.Cut(sender, "@")
	var localRule, domainRule *Rule
	for i := range rules {
		r := rules[i]
		switch r.Scope {
		case "sender":
			if r.Value == sender {
				return r, true
			}
		case "local":
			if local != "" && r.Value == local && localRule == nil {
				localRule = &rules[i]
			}
		case "domain":
			if domain != "" && (domain == r.Value || strings.HasSuffix(domain, "."+r.Value)) && domainRule == nil {
				domainRule = &rules[i]
			}
		}
	}
	if localRule != nil {
		return *localRule, true
	}
	if domainRule != nil {
		return *domainRule, true
	}
	return Rule{}, false
}
