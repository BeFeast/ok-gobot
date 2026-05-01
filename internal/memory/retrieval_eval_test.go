package memory

import (
	"context"
	"strings"
	"testing"
)

func TestMemoryRetrievalEvaluationHarness(t *testing.T) {
	report, err := RunRetrievalEval(context.Background(), RetrievalEvalOptions{})
	if err != nil {
		t.Fatalf("RunRetrievalEval failed: %v", err)
	}
	if len(report.QueryResults) < 10 {
		t.Fatalf("retrieval evaluation query count = %d, want at least 10", len(report.QueryResults))
	}
	if !report.Passed() {
		t.Fatalf("memory retrieval evaluation failed:\n%s", report.FormatText())
	}
	if report.PrivacyLeaks != 0 {
		t.Fatalf("privacy leaks should fail the eval, got %d", report.PrivacyLeaks)
	}
	if report.MissingCitations != 0 || report.FreshnessFailures != 0 {
		t.Fatalf("unexpected citation/freshness regression:\n%s", report.FormatText())
	}
	t.Logf("memory retrieval evaluation passed:\n%s", report.FormatGateText())
}

func TestRetrievalEvalPrivacyLeaksFailReport(t *testing.T) {
	report, err := RunRetrievalEval(context.Background(), RetrievalEvalOptions{
		Queries: []RetrievalEvalQuery{{
			Name:  "privacy-leak-detection-control",
			Mode:  RetrievalEvalModeVector,
			Query: "blue badger atlas escalation route",
			TopK:  5,
			Forbidden: []RetrievalEvalCitation{{
				SourceFile:   "memory/chats/222/project-atlas-secret.md",
				HeaderPath:   "Chat 222 Memory > Project Atlas Secret",
				ChunkOrdinal: 0,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("RunRetrievalEval failed: %v", err)
	}
	if report.Passed() || report.PrivacyLeaks == 0 {
		t.Fatalf("expected privacy leak failure, got:\n%s", report.FormatText())
	}
	if !strings.Contains(report.FormatText(), "privacy_leak") {
		t.Fatalf("report should show privacy_leak class:\n%s", report.FormatText())
	}
}

func TestRetrievalEvalReportShowsMissingCitationAndFreshnessFailures(t *testing.T) {
	report, err := RunRetrievalEval(context.Background(), RetrievalEvalOptions{
		Queries: []RetrievalEvalQuery{
			{
				Name:  "missing-citation-control",
				Mode:  RetrievalEvalModeFTSBM25,
				Query: "capybara release codename",
				TopK:  1,
				Expected: []RetrievalEvalCitation{{
					SourceFile:   "missing.md",
					HeaderPath:   "Missing",
					ChunkOrdinal: 0,
				}},
			},
			{
				Name:  "freshness-control",
				Mode:  RetrievalEvalModeHybrid,
				Query: "current billing processor owner after compaction flush",
				TopK:  3,
				Expected: []RetrievalEvalCitation{{
					SourceFile:   retrievalEvalFreshnessSource,
					HeaderPath:   "Daily Memory: 2026-04-13 > Compaction",
					ChunkOrdinal: 0,
				}},
				ForbiddenContent: []RetrievalEvalContentRule{{Text: "Riley", FailureClass: "freshness"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("RunRetrievalEval failed: %v", err)
	}
	text := report.FormatText()
	for _, want := range []string{"Missing citations: 1", "Freshness failures: 1", "missing_citation", "freshness", "stale_content"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in report:\n%s", want, text)
		}
	}
}

func TestRetrievalEvalGateReportIsConciseAndActionable(t *testing.T) {
	report, err := RunRetrievalEval(context.Background(), RetrievalEvalOptions{
		Queries: []RetrievalEvalQuery{{
			Name:  "missing-citation-control",
			Mode:  RetrievalEvalModeVector,
			Query: "capybara release codename",
			TopK:  1,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "missing.md",
				HeaderPath:   "Missing",
				ChunkOrdinal: 0,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("RunRetrievalEval failed: %v", err)
	}

	text := report.FormatGateText()
	for _, want := range []string{
		"Memory Retrieval Eval Gate: fail",
		"FAIL missing-citation-control",
		"classes=missing_citation",
		"recall=0.00",
		"precision-ish=0.00",
		"fallback=none",
		"latency=",
		"missing: missing.md :: Missing#0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in gate report:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"expected:\n", "hits:\n"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("gate report should stay concise without %q:\n%s", notWant, text)
		}
	}
}

func TestRetrievalEvalUnexpectedFallbackFailsReport(t *testing.T) {
	report, err := RunRetrievalEval(context.Background(), RetrievalEvalOptions{
		Queries: []RetrievalEvalQuery{{
			Name:  "unexpected-fallback-control",
			Mode:  RetrievalEvalModeQMDUnavailableFallback,
			Query: "quarto notebook recall hybrid retrieval qmd",
			TopK:  3,
			Expected: []RetrievalEvalCitation{{
				SourceFile:   "research/phoenix-eval.qmd",
				HeaderPath:   "Research > Phoenix QMD Plan",
				ChunkOrdinal: 0,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("RunRetrievalEval failed: %v", err)
	}
	if report.Passed() || report.FallbackFailures == 0 {
		t.Fatalf("expected unexpected fallback failure, got:\n%s", report.FormatGateText())
	}
	text := report.FormatGateText()
	for _, want := range []string{"FAIL unexpected-fallback-control", "classes=unexpected_fallback", "fallback=used", "fallback_reason: qmd backend unavailable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in report:\n%s", want, text)
		}
	}
}
