package maestro

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakeSource struct {
	issues  []Issue
	deps    map[int]DependencyStatus
	depErrs map[int]error
	calls   []DependencyRef
}

func (f *fakeSource) ListOpenIssues(_ context.Context, _ string, _ int) ([]Issue, error) {
	return f.issues, nil
}

func (f *fakeSource) ResolveDependency(_ context.Context, _ string, ref DependencyRef) (DependencyStatus, error) {
	f.calls = append(f.calls, ref)
	if err := f.depErrs[ref.Number]; err != nil {
		return DependencyStatus{}, err
	}
	if status, ok := f.deps[ref.Number]; ok {
		return status, nil
	}
	return DependencyStatus{Ref: ref, State: "open"}, nil
}

func TestEvaluateRequiresReadyLabelByDefault(t *testing.T) {
	t.Parallel()

	source := &fakeSource{}
	decision := Evaluate(context.Background(), []Issue{
		{Number: 1, Title: "not ready", Labels: []string{"bug"}},
		{Number: 2, Title: "ready", Labels: []string{"ready"}},
	}, source, Policy{})

	if decision.Next == nil || decision.Next.Issue.Number != 2 {
		t.Fatalf("next issue = %#v, want #2", decision.Next)
	}
	if len(decision.Skipped) != 1 {
		t.Fatalf("skipped count = %d, want 1", len(decision.Skipped))
	}
	assertReasonContains(t, decision.Skipped[0].SkipReasons, `missing ready label "ready"`)
}

func TestEvaluateDoesNotSelectDemoWaveWithoutReady(t *testing.T) {
	t.Parallel()

	decision := Evaluate(context.Background(), []Issue{{
		Number: 13,
		Title:  "demo wave task",
		Labels: []string{"demo-wave"},
	}}, &fakeSource{}, Policy{})

	if decision.Next != nil {
		t.Fatalf("next issue = %#v, want none", decision.Next)
	}
	assertReasonContains(t, decision.Skipped[0].SkipReasons, `missing ready label "ready"`)
}

func TestEvaluateHardExcludesBlockedLabel(t *testing.T) {
	t.Parallel()

	source := &fakeSource{}
	decision := Evaluate(context.Background(), []Issue{
		{Number: 1, Title: "blocked", Labels: []string{"ready", "blocked"}},
		{Number: 2, Title: "ready", Labels: []string{"ready"}},
	}, source, Policy{})

	if decision.Next == nil || decision.Next.Issue.Number != 2 {
		t.Fatalf("next issue = %#v, want #2", decision.Next)
	}
	assertReasonContains(t, decision.Skipped[0].SkipReasons, `hard-exclude label "blocked"`)
}

func TestEvaluateContinuesScanningSkippedCandidatesAfterNext(t *testing.T) {
	t.Parallel()

	source := &fakeSource{deps: map[int]DependencyStatus{
		9: {State: "open"},
	}}
	decision := Evaluate(context.Background(), []Issue{
		{Number: 1, Title: "ready", Labels: []string{"ready"}},
		{Number: 2, Title: "blocked", Labels: []string{"ready", "blocked"}},
		{Number: 3, Title: "depends", Labels: []string{"ready"}, Body: "Depends on: #9"},
	}, source, Policy{})

	if decision.Next == nil || decision.Next.Issue.Number != 1 {
		t.Fatalf("next issue = %#v, want #1", decision.Next)
	}
	if len(decision.Skipped) != 2 {
		t.Fatalf("skipped count = %d, want 2", len(decision.Skipped))
	}
	assertReasonContains(t, decision.Skipped[0].SkipReasons, `hard-exclude label "blocked"`)
	assertReasonContains(t, decision.Skipped[1].SkipReasons, "dependency #9 is open")
}

func TestEvaluateSkipsMissingDependency(t *testing.T) {
	t.Parallel()

	source := &fakeSource{deps: map[int]DependencyStatus{
		9: {State: "open"},
	}}
	decision := Evaluate(context.Background(), []Issue{{
		Number: 1,
		Title:  "depends",
		Labels: []string{"ready"},
		Body:   "Depends on: #9",
	}}, source, Policy{})

	if decision.Next != nil {
		t.Fatalf("next issue = %#v, want none", decision.Next)
	}
	if len(decision.Skipped) != 1 {
		t.Fatalf("skipped count = %d, want 1", len(decision.Skipped))
	}
	assertReasonContains(t, decision.Skipped[0].SkipReasons, "dependency #9 is open")
}

func TestEvaluateAllowsClosedDependency(t *testing.T) {
	t.Parallel()

	source := &fakeSource{deps: map[int]DependencyStatus{
		9: {State: "closed"},
	}}
	decision := Evaluate(context.Background(), []Issue{{
		Number: 1,
		Title:  "depends",
		Labels: []string{"ready"},
		Body:   "Depends on: #9",
	}}, source, Policy{})

	if decision.Next == nil || decision.Next.Issue.Number != 1 {
		t.Fatalf("next issue = %#v, want #1", decision.Next)
	}
	if len(source.calls) != 1 || source.calls[0].Number != 9 {
		t.Fatalf("dependency calls = %#v, want #9", source.calls)
	}
}

func TestParseDependencyRefsIgnoresExamplesAndCode(t *testing.T) {
	t.Parallel()

	body := strings.Join([]string{
		"This issue mentions `Depends on: #9` inline.",
		"",
		"```md",
		"Depends on: #10",
		"```",
		"",
		"Example:",
		"Depends on: #11",
		"",
		"Previously depended on: #12",
	}, "\n")

	refs := ParseDependencyRefs(body)
	if len(refs) != 0 {
		t.Fatalf("refs = %#v, want none", refs)
	}
}

func TestParseDependencyRefsIgnoresMultilineHTMLComments(t *testing.T) {
	t.Parallel()

	body := strings.Join([]string{
		"<!--",
		"Depends on: #9",
		"-->",
		"Depends on: #10",
	}, "\n")

	refs := ParseDependencyRefs(body)
	if len(refs) != 1 || refs[0].String() != "#10" {
		t.Fatalf("refs = %#v, want only #10", refs)
	}
}

func TestEvaluateOverrideSelectsFirstIssueAndShowsBypassedReasons(t *testing.T) {
	t.Parallel()

	source := &fakeSource{deps: map[int]DependencyStatus{
		9: {State: "open"},
	}}
	decision := Evaluate(context.Background(), []Issue{{
		Number: 1,
		Title:  "force me",
		Labels: []string{"blocked"},
		Body:   "Depends on: #9",
	}}, source, Policy{Override: true, OverrideReason: "maintainer reviewed"})

	if decision.Next == nil || decision.Next.Issue.Number != 1 {
		t.Fatalf("next issue = %#v, want #1", decision.Next)
	}
	if !decision.Next.OverrideUsed {
		t.Fatalf("expected override to be visible: %#v", decision.Next)
	}
	if len(decision.Skipped) != 0 {
		t.Fatalf("skipped count = %d, want 0", len(decision.Skipped))
	}
	assertReasonContains(t, decision.Next.OverrideReasons, `missing ready label "ready"`)
	assertReasonContains(t, decision.Next.OverrideReasons, `hard-exclude label "blocked"`)
	assertReasonContains(t, decision.Next.OverrideReasons, "dependency #9 is open")
}

func TestParseDependencyRefsDedicatedLines(t *testing.T) {
	t.Parallel()

	refs := ParseDependencyRefs(strings.Join([]string{
		"- Depends on: #3, BeFeast/ok-gobot#4",
		"1. blocked by #5",
	}, "\n"))
	if len(refs) != 3 {
		t.Fatalf("refs = %#v, want 3 refs", refs)
	}
	want := []string{"#3", "BeFeast/ok-gobot#4", "#5"}
	for i := range want {
		if refs[i].String() != want[i] {
			t.Fatalf("ref[%d] = %q, want %q", i, refs[i].String(), want[i])
		}
	}
}

func assertReasonContains(t *testing.T, reasons []string, want string) {
	t.Helper()
	for _, reason := range reasons {
		if strings.Contains(reason, want) {
			return
		}
	}
	t.Fatalf("reasons %v do not contain %q", reasons, want)
}

func ExampleParseDependencyRefs() {
	refs := ParseDependencyRefs("Depends on: #353")
	fmt.Println(refs[0].String())
	// Output: #353
}
