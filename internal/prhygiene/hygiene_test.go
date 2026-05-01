package prhygiene

import (
	"testing"
	"time"
)

func TestDiagnoseAllDistinguishesPRBlockers(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	prs := []PullRequest{
		{
			Number:     242,
			Title:      "feat: implement Verification Gate and Paranoid Protocol (#195)",
			State:      "OPEN",
			MergeState: "DIRTY",
			UpdatedAt:  time.Date(2026, 3, 25, 13, 56, 42, 0, time.UTC),
		},
		{
			Number:     279,
			Title:      "test(agent): expand reflection test coverage (#243)",
			State:      "OPEN",
			MergeState: "CLEAN",
			UpdatedAt:  time.Date(2026, 3, 30, 20, 20, 23, 0, time.UTC),
		},
		{
			Number: 301,
			State:  "OPEN",
			Checks: []Check{{Name: "test", Conclusion: "failure"}},
		},
		{
			Number:         302,
			State:          "OPEN",
			ReviewDecision: "CHANGES_REQUESTED",
		},
		{
			Number: 303,
			State:  "OPEN",
			Checks: []Check{{Name: "Greptile Review", Conclusion: "failure"}},
		},
	}

	blockers := DiagnoseAll(prs, Options{Now: now, StaleAfter: DefaultStaleAfter})
	assertBlockerKind(t, blockers, 242, KindNonMergeable)
	assertBlockerKind(t, blockers, 279, KindStale)
	assertBlockerKind(t, blockers, 301, KindCI)
	assertBlockerKind(t, blockers, 302, KindReview)
	assertBlockerKind(t, blockers, 303, KindGreptile)
}

func TestDiagnoseIgnoresHealthyRecentPR(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	_, ok := Diagnose(PullRequest{
		Number:     44,
		State:      "OPEN",
		MergeState: "CLEAN",
		UpdatedAt:  now.Add(-time.Hour),
		Checks:     []Check{{Name: "test", Conclusion: "success"}},
	}, Options{Now: now})
	if ok {
		t.Fatal("expected no blocker for healthy recent PR")
	}
}

func TestDiagnoseTreatsHumanGreptileMentionAsReviewFeedback(t *testing.T) {
	blocker, ok := Diagnose(PullRequest{
		Number: 370,
		State:  "OPEN",
		Reviews: []Review{{
			Author: "human-reviewer",
			State:  "CHANGES_REQUESTED",
			Body:   "Please also address the Greptile findings.",
		}},
	}, Options{})
	if !ok {
		t.Fatal("expected review blocker")
	}
	if blocker.Kind != KindReview {
		t.Fatalf("blocker kind = %q, want %q", blocker.Kind, KindReview)
	}
}

func TestBlockerFingerprintIsStableAcrossPolls(t *testing.T) {
	updated := time.Date(2026, 3, 30, 20, 20, 23, 0, time.UTC)
	pr := PullRequest{Number: 279, State: "OPEN", MergeState: "CLEAN", UpdatedAt: updated}
	first, ok := Diagnose(pr, Options{Now: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)})
	if !ok {
		t.Fatal("expected stale blocker")
	}
	second, ok := Diagnose(pr, Options{Now: time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)})
	if !ok {
		t.Fatal("expected stale blocker on second poll")
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Fatalf("fingerprint changed: %q vs %q", first.Fingerprint(), second.Fingerprint())
	}
	if first.Reason != second.Reason {
		t.Fatalf("reason changed: %q vs %q", first.Reason, second.Reason)
	}
}

func assertBlockerKind(t *testing.T, blockers []Blocker, number int, kind Kind) {
	t.Helper()
	for _, blocker := range blockers {
		if blocker.Number == number {
			if blocker.Kind != kind {
				t.Fatalf("PR #%d kind = %q, want %q", number, blocker.Kind, kind)
			}
			if blocker.State != "OPEN" || blocker.Reason == "" {
				t.Fatalf("PR #%d blocker missing state/reason: %+v", number, blocker)
			}
			return
		}
	}
	t.Fatalf("PR #%d not found in blockers: %+v", number, blockers)
}
