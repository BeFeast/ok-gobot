package deepthink

import "testing"

func testMatcher() *Matcher {
	return NewMatcher([]string{"подумай хорошо", "подумай как следует", "подумай глубоко", "think hard", "think carefully", "ultrathink", "", "  "})
}

func TestMatcherMatchesPrefixAndStripsPhrase(t *testing.T) {
	m := testMatcher()
	cases := map[string]string{
		"подумай хорошо: сколько будет 2+2?":             "сколько будет 2+2?",
		"Подумай хорошо, сколько будет 2+2?":             "сколько будет 2+2?",
		"ПОДУМАЙ ХОРОШО — сколько будет 2+2?":            "сколько будет 2+2?",
		"think hard. What is the capital of Peru?":       "What is the capital of Peru?",
		"Think carefully:\nWhat is the capital of Peru?": "What is the capital of Peru?",
		"«подумай как следует» почему небо синее":        "почему небо синее",
		"Ultrathink! Compare Rust and Go for a daemon":   "Compare Rust and Go for a daemon",
	}
	for text, want := range cases {
		got, ok := m.Match(text)
		if !ok {
			t.Errorf("%q: no match", text)
			continue
		}
		if got.Rest != want {
			t.Errorf("%q: rest = %q, want %q", text, got.Rest, want)
		}
		if got.AtEnd {
			t.Errorf("%q: reported as suffix match", text)
		}
	}
}

func TestMatcherMatchesSuffixAndKeepsQuestionMark(t *testing.T) {
	m := testMatcher()
	cases := map[string]string{
		"сколько будет 2+2? подумай хорошо":        "сколько будет 2+2?",
		"сколько будет 2+2, подумай хорошо!":       "сколько будет 2+2",
		"What is the capital of Peru? Think hard.": "What is the capital of Peru?",
		"Compare Rust and Go — ultrathink":         "Compare Rust and Go",
	}
	for text, want := range cases {
		got, ok := m.Match(text)
		if !ok {
			t.Errorf("%q: no match", text)
			continue
		}
		if got.Rest != want {
			t.Errorf("%q: rest = %q, want %q", text, got.Rest, want)
		}
		if !got.AtEnd {
			t.Errorf("%q: not reported as suffix match", text)
		}
	}
}

func TestMatcherIgnoresPhraseInsideSentence(t *testing.T) {
	m := testMatcher()
	for _, text := range []string{
		"я не подумал хорошо о том, что делать",
		"вчера я сказал подумай хорошо и ушёл",
		"please think hard about it and then reply",
		"think harder than last time", // "hard" followed by a letter is not a boundary
		"ultrathinking is a habit",    // same for the suffix form
		"тут ни одной фразы",
		"",
		"   ",
	} {
		if got, ok := m.Match(text); ok {
			t.Errorf("%q: unexpected match %+v", text, got)
		}
	}
}

func TestMatcherPhraseAloneKeepsOriginalText(t *testing.T) {
	m := testMatcher()
	got, ok := m.Match("  подумай хорошо  ")
	if !ok {
		t.Fatal("no match")
	}
	if got.Rest != "подумай хорошо" {
		t.Fatalf("rest = %q, want the original phrase", got.Rest)
	}
}

func TestMatcherPrefersLongestPhrase(t *testing.T) {
	m := NewMatcher([]string{"подумай", "подумай как следует"})
	got, ok := m.Match("подумай как следует: вопрос")
	if !ok || got.Phrase != "подумай как следует" || got.Rest != "вопрос" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestMatcherDisabledWhenEmpty(t *testing.T) {
	var nilMatcher *Matcher
	if nilMatcher.Enabled() {
		t.Fatal("nil matcher reported enabled")
	}
	if _, ok := nilMatcher.Match("think hard: x"); ok {
		t.Fatal("nil matcher matched")
	}
	empty := NewMatcher(nil)
	if empty.Enabled() {
		t.Fatal("empty matcher reported enabled")
	}
	if _, ok := empty.Match("think hard: x"); ok {
		t.Fatal("empty matcher matched")
	}
}
