package memory

import "strings"

// MaxComposedQueryTerms bounds how many distinct terms one natural-language
// question contributes to a search. Questions longer than this are dominated
// by their leading terms anyway, and every extra term costs a query.
const MaxComposedQueryTerms = 12

// queryStopwords holds Russian and English function words plus the verbs and
// interrogatives that frame a question without describing its subject.
//
// Dropping them is the single highest-leverage change in this package: a
// natural-language question such as "как я настраивал бэкап на сервере?"
// contains three content words and five words that appear in almost every
// note. Searching the raw question ranks notes by how many stopwords they
// contain, which is noise; searching only the content words ranks them by
// subject.
var queryStopwords = func() map[string]struct{} {
	words := []string{
		// Russian — pronouns, prepositions, particles, conjunctions.
		"а", "без", "более", "больше", "будет", "будто", "бы", "был", "была", "были",
		"было", "быть", "в", "вам", "вас", "ведь", "весь", "во", "вот", "все", "всего",
		"всех", "всю", "всегда", "всё", "вы", "где", "да", "даже", "два", "для", "до",
		"его", "ее", "ей", "ему", "если", "есть", "ещё", "еще", "же", "за", "здесь",
		"и", "из", "или", "им", "их", "к", "как", "какая", "какие", "каких", "какое",
		"каком", "какой", "когда", "конечно", "кто", "куда", "ли", "лучше", "между",
		"меня", "мне", "много", "может", "можно", "мой", "моя", "мы", "на", "над",
		"надо", "наконец", "нас", "не", "него", "неё", "нее", "ней", "нельзя", "нет",
		"ни", "нибудь", "никогда", "ним", "них", "но", "ну", "о", "об", "один", "он",
		"она", "они", "опять", "от", "перед", "по", "под", "после", "потом", "потому",
		"почему", "почти", "при", "про", "раз", "разве", "с", "сам", "свою", "себе",
		"себя", "сколько", "снова", "со", "совсем", "так", "такой", "там", "тебя",
		"тем", "теперь", "то", "тоже", "того", "тогда", "той", "только", "том", "тот",
		"три", "тут", "ты", "у", "уж", "уже", "хоть", "хорошо", "чего", "чем", "через",
		"что", "чтоб", "чтобы", "чуть", "эта", "эти", "это", "этого", "этой", "этом",
		"этот", "эту", "я",
		// Russian — question framing verbs. "Найди заметку про X" is a request,
		// not a description of X.
		"дай", "делать", "зачем", "искать", "най", "найди", "найти", "написать",
		"объясни", "покажи", "поясни", "расскажи", "сделать", "скажи", "список",
		"узнать",

		// English — function words.
		"a", "about", "above", "after", "again", "against", "all", "am", "an", "and",
		"any", "are", "as", "at", "be", "because", "been", "before", "being", "below",
		"between", "both", "but", "by", "can", "cannot", "could", "did", "do", "does",
		"doing", "down", "during", "each", "few", "for", "from", "further", "had",
		"has", "have", "having", "he", "her", "here", "hers", "him", "his", "how",
		"i", "if", "in", "into", "is", "it", "its", "just", "me", "more", "most", "my",
		"no", "nor", "not", "now", "of", "off", "on", "once", "only", "or", "other",
		"our", "out", "over", "own", "same", "she", "should", "so", "some", "such",
		"than", "that", "the", "their", "them", "then", "there", "these", "they",
		"this", "those", "through", "to", "too", "under", "until", "up", "very", "was",
		"we", "were", "what", "whats", "when", "where", "which", "while", "who", "whom",
		"why", "will", "with", "would", "you", "your",
		// English — question framing verbs.
		"explain", "find", "get", "give", "list", "search", "show", "tell",
	}
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	return set
}()

// IsQueryStopword reports whether term is a Russian or English function word
// that should not drive retrieval. term is compared case-insensitively.
func IsQueryStopword(term string) bool {
	_, ok := queryStopwords[strings.ToLower(strings.TrimSpace(term))]
	return ok
}

// ComposeQueryTerms turns a natural-language question into the content terms
// worth searching for. It tokenizes on letters/digits/underscore, lowercases,
// deduplicates, drops stopwords and single-character tokens, and caps the
// result at MaxComposedQueryTerms.
//
// When every token is a stopword the unfiltered tokens are returned instead,
// so a deliberate search for "how to" still does something rather than
// silently matching nothing.
func ComposeQueryTerms(query string) []string {
	raw := memorySearchTokenRegexp.FindAllString(query, -1)
	if len(raw) == 0 {
		return nil
	}

	all := make([]string, 0, len(raw))
	content := make([]string, 0, len(raw))
	seenAll := make(map[string]struct{}, len(raw))
	seenContent := make(map[string]struct{}, len(raw))

	for _, token := range raw {
		term := strings.ToLower(strings.TrimSpace(token))
		if term == "" {
			continue
		}
		if _, dup := seenAll[term]; !dup {
			seenAll[term] = struct{}{}
			all = append(all, term)
		}
		if len([]rune(term)) < 2 {
			continue
		}
		if _, stop := queryStopwords[term]; stop {
			continue
		}
		if _, dup := seenContent[term]; dup {
			continue
		}
		seenContent[term] = struct{}{}
		content = append(content, term)
	}

	if len(content) == 0 {
		content = all
	}
	if len(content) > MaxComposedQueryTerms {
		content = content[:MaxComposedQueryTerms]
	}
	return content
}
