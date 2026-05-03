package bot

import "testing"

func TestDetectNativeSkillIntentRoutesKaraokePhrases(t *testing.T) {
	for _, input := range []string{
		"сделай караоке https://youtu.be/dQw4w9WgXcQ",
		"убери вокал из https://www.youtube.com/watch?v=dQw4w9WgXcQ пожалуйста",
		"make instrumental https://youtu.be/dQw4w9WgXcQ",
	} {
		intent, ok := detectNativeSkillIntent(input)
		if !ok {
			t.Fatalf("expected intent for %q", input)
		}
		if intent.Skill != "karaoke" {
			t.Fatalf("skill=%q, want karaoke for %q", intent.Skill, input)
		}
		if intent.URL == "" {
			t.Fatalf("expected URL for %q", input)
		}
	}
}

func TestDetectNativeSkillIntentRoutesVideoSummaryPhrases(t *testing.T) {
	for _, input := range []string{
		"сделай конспект https://youtu.be/UF8uR6Z6KLc",
		"нужна транскрипция https://www.youtube.com/watch?v=UF8uR6Z6KLc",
		"summarize https://youtu.be/UF8uR6Z6KLc into obsidian",
	} {
		intent, ok := detectNativeSkillIntent(input)
		if !ok {
			t.Fatalf("expected intent for %q", input)
		}
		if intent.Skill != "video-summary" {
			t.Fatalf("skill=%q, want video-summary for %q", intent.Skill, input)
		}
		if intent.URL == "" {
			t.Fatalf("expected URL for %q", input)
		}
	}
}

func TestDetectNativeSkillIntentRequiresURLAndUnambiguousIntent(t *testing.T) {
	cases := []string{
		"что ты помнишь про video_summary и karaoke?",
		"https://youtu.be/dQw4w9WgXcQ",
		"сделай karaoke и summary https://youtu.be/dQw4w9WgXcQ",
		"сделай конспект https://youtu.be/a и https://youtu.be/b",
		"сделай конспект https://youtu.be/",
		"сделай конспект https://www.youtube.com/watch?v=",
	}
	for _, input := range cases {
		if intent, ok := detectNativeSkillIntent(input); ok {
			t.Fatalf("unexpected intent %+v for %q", intent, input)
		}
	}
}

func TestSingleYouTubeURLTrimsPunctuationAndRejectsMultiple(t *testing.T) {
	got, ok := singleYouTubeURL("сделай конспект (https://youtu.be/UF8uR6Z6KLc).")
	if !ok {
		t.Fatal("expected URL")
	}
	if got != "https://youtu.be/UF8uR6Z6KLc" {
		t.Fatalf("got %q", got)
	}

	if _, ok := singleYouTubeURL("https://youtu.be/a https://youtu.be/b"); ok {
		t.Fatal("expected multiple URLs to be rejected")
	}
}
