package memory

import (
	"strings"
	"testing"
)

func TestComposeQueryTermsDropsRussianAndEnglishStopwords(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []string
		gone  []string
	}{
		{
			name:  "russian question",
			query: "Как я настраивал backup для Proxmox на сервере?",
			want:  []string{"настраивал", "backup", "proxmox", "сервере"},
			gone:  []string{"как", "я", "для", "на"},
		},
		{
			name:  "english question",
			query: "How do I configure the ZFS snapshot retention?",
			want:  []string{"configure", "zfs", "snapshot", "retention"},
			gone:  []string{"how", "do", "i", "the"},
		},
		{
			name:  "request framing is not subject matter",
			query: "Покажи заметки про Maestro routing",
			want:  []string{"заметки", "maestro", "routing"},
			gone:  []string{"покажи", "про"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComposeQueryTerms(tc.query)
			joined := strings.Join(got, " ")
			for _, want := range tc.want {
				if !containsTerm(got, want) {
					t.Errorf("terms %v missing %q", got, want)
				}
			}
			for _, gone := range tc.gone {
				if containsTerm(got, gone) {
					t.Errorf("stopword %q survived in %v", gone, got)
				}
			}
			if strings.TrimSpace(joined) == "" {
				t.Fatal("no terms produced")
			}
		})
	}
}

func TestComposeQueryTermsNormalizesAndBounds(t *testing.T) {
	if got := ComposeQueryTerms("Backup BACKUP backup"); len(got) != 1 || got[0] != "backup" {
		t.Errorf("dedup/lowercase failed: %v", got)
	}
	if got := ComposeQueryTerms(""); got != nil {
		t.Errorf("empty query = %v, want nil", got)
	}
	if got := ComposeQueryTerms("!!! ???"); got != nil {
		t.Errorf("punctuation-only query = %v, want nil", got)
	}

	var many []string
	for i := 0; i < MaxComposedQueryTerms*3; i++ {
		many = append(many, string(rune('a'+i%26))+string(rune('a'+i/26))+"term")
	}
	if got := ComposeQueryTerms(strings.Join(many, " ")); len(got) > MaxComposedQueryTerms {
		t.Errorf("term count %d exceeds cap %d", len(got), MaxComposedQueryTerms)
	}
}

// A query made only of stopwords must still search for something rather than
// silently reducing to zero terms.
func TestComposeQueryTermsFallsBackWhenEverythingIsAStopword(t *testing.T) {
	got := ComposeQueryTerms("как что где")
	if len(got) == 0 {
		t.Fatal("all-stopword query produced no terms")
	}
}

func TestBuildMemoryFTSQueryUsesComposedTermsWithOrSemantics(t *testing.T) {
	got := buildMemoryFTSQuery("Как я настраивал backup для Proxmox?")
	if !strings.Contains(got, " OR ") {
		t.Errorf("FTS query %q lost OR semantics", got)
	}
	if strings.Contains(got, `"как"`) || strings.Contains(got, `"для"`) {
		t.Errorf("FTS query %q still contains stopwords", got)
	}
	for _, want := range []string{`"backup"`, `"proxmox"`} {
		if !strings.Contains(got, want) {
			t.Errorf("FTS query %q missing %s", got, want)
		}
	}
	if got := buildMemoryFTSQuery(""); got != "" {
		t.Errorf("empty query produced %q", got)
	}
}

func containsTerm(terms []string, want string) bool {
	for _, term := range terms {
		if term == want {
			return true
		}
	}
	return false
}
