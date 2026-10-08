package gmailtriage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunAppliesAssistantPolicy(t *testing.T) {
	// Thursday 2026-10-08, 09:00 UTC.
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	news := msg("n1", "Shop <news@shop.test>", "Sale", "50% off", now.Add(-2*time.Hour))
	news.ListUnsubscribe = "<mailto:unsub@shop.test>"
	mine := msg("w1", ownerAddr, "Proposal", "Any update?", now.AddDate(0, 0, -7), "SENT")
	mine.To = "Bob <bob@partner.test>"
	mb := newFakeMailbox(
		thread("news", news),
		thread("ask", msg("a1", "Dana <dana@acme.test>", "Contract", "Can you confirm by Friday?", now.Add(-time.Hour))),
		thread("wait", msg("w0", "Bob <bob@partner.test>", "Proposal", "Send me the proposal", now.AddDate(0, 0, -9)), mine),
	)
	llm := &fakeLLM{verdicts: map[string]llmVerdict{"ask": {Bucket: BucketReply, Why: "просит подтвердить", Draft: "Подтверждаю."}}}
	svc := newTestService(t, assistantConfig(), mb, llm, now)
	p := svc.Profiles()[0]

	d, err := svc.Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !d.FirstRun || len(d.Errors) != 0 {
		t.Fatalf("first=%v errors=%v", d.FirstRun, d.Errors)
	}
	buckets := map[string]Item{}
	for _, it := range append(append([]Item{}, d.Items...), d.Quiet...) {
		buckets[it.ThreadID] = it
	}
	if it := buckets["news"]; it.Bucket != BucketBulk || it.Source != "prefilter" || !it.Archived {
		t.Errorf("newsletter = %+v, want archived bulk from prefilter", it)
	}
	if it := buckets["ask"]; it.Bucket != BucketReply || it.Draft != "Подтверждаю." {
		t.Errorf("person thread = %+v", it)
	}
	if it := buckets["wait"]; it.Bucket != BucketWaiting || it.Sender != "bob@partner.test" || it.Source != "waiting" {
		t.Errorf("waiting thread = %+v", it)
	}
	// Only the human thread went to the LLM.
	if llm.calls() != 1 || strings.Contains(llm.users[0], "50% off") {
		t.Errorf("LLM saw prefiltered mail or was called %d times", llm.calls())
	}
	if !strings.Contains(llm.systems[0], "SKILL BODY") {
		t.Error("SKILL.md body missing from classifier prompt")
	}
	log := mb.modifyLog()
	if !strings.Contains(log, "news +Triage/Bulk -") || !strings.Contains(log, "INBOX,UNREAD") {
		t.Errorf("bulk not archived and marked read:\n%s", log)
	}
	if strings.Contains(log, "ask +Triage/Reply -Triage/Action,Triage/Waiting,Triage/Meetings,Triage/FYI,Triage/Bulk,INBOX") {
		t.Errorf("a person's mail left the inbox:\n%s", log)
	}
	if !strings.Contains(log, "ask +Triage/Reply") {
		t.Errorf("person thread not labelled:\n%s", log)
	}

	// Delivered items do not repeat; an unchanged mailbox yields an empty digest.
	for _, it := range append(d.Items, d.Quiet...) {
		if err := svc.Store().MarkReported(it.ID, 0); err != nil {
			t.Fatal(err)
		}
	}
	d2, err := svc.Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !d2.Empty() || len(d2.Quiet) != 0 || d2.FirstRun {
		t.Fatalf("second digest = %+v", d2)
	}
	if !strings.Contains(mb.searches[len(mb.searches)-2], "after:") {
		t.Errorf("second search is not incremental: %q", mb.searches[len(mb.searches)-2])
	}
}

func TestFirstRunUsesLookbackForSalesProfile(t *testing.T) {
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	mb := newFakeMailbox(thread("t1", msg("m1", "Fan <fan@example.org>", "ur music saved my life", "thank you", now)))
	llm := &fakeLLM{verdicts: map[string]llmVerdict{"t1": {Bucket: BucketIgnore, Why: "fan mail"}}}
	svc := newTestService(t, readOnlyConfig(), mb, llm, now)
	p := svc.Profiles()[0]
	if _, err := svc.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if mb.searches[0] != "(label:info) newer_than:90d" {
		t.Fatalf("first search = %q", mb.searches[0])
	}
	if len(mb.modifies) != 0 || len(mb.labels) != 0 {
		t.Fatalf("read-only profile changed labels: %v %v", mb.modifies, mb.labels)
	}
}

// A button correction becomes a sender rule; the next mail from that sender is
// classified by the rule without the LLM.
func TestCorrectionChangesNextClassification(t *testing.T) {
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	mb := newFakeMailbox(thread("t1", msg("m1", "Label <ar@bigrecords.test>", "Licensing", "We love the track", now)))
	llm := &fakeLLM{verdicts: map[string]llmVerdict{"t1": {Bucket: BucketIgnore, Why: "fan mail"}}}
	svc := newTestService(t, readOnlyConfig(), mb, llm, now)
	p := svc.Profiles()[0]
	ctx := context.Background()

	d, err := svc.Run(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	item := d.Quiet[0]
	fixed, err := svc.Recategorize(ctx, p, item.ID, BucketSales, ScopeDomain)
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Bucket != BucketSales {
		t.Fatalf("recategorized bucket = %s", fixed.Bucket)
	}
	_ = svc.Store().MarkReported(item.ID, 0)

	mb.add(thread("t2", msg("m2", "Other <sync@bigrecords.test>", "Another one", "Interested in a sync deal", now.Add(time.Minute))))
	callsBefore := llm.calls()
	svc.now = func() time.Time { return now.Add(time.Hour) }
	d2, err := svc.Run(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.Items) != 1 || d2.Items[0].Bucket != BucketSales || d2.Items[0].Source != "rule" {
		t.Fatalf("next digest = %+v", d2.Items)
	}
	if llm.calls() != callsBefore {
		t.Error("rule-matched mail still went to the LLM")
	}
	// The correction is also a few-shot example for mail the rules do not cover.
	mb.add(thread("t3", msg("m3", "Someone <x@else.test>", "hello", "hi", now.Add(2*time.Minute))))
	svc.now = func() time.Time { return now.Add(2 * time.Hour) }
	if _, err := svc.Run(ctx, p); err != nil {
		t.Fatal(err)
	}
	last := llm.systems[len(llm.systems)-1]
	if !strings.Contains(last, "ar@bigrecords.test") || !strings.Contains(last, "domain bigrecords.test → sales") {
		t.Errorf("classifier prompt lacks learned rule/example:\n%s", last)
	}
}

func TestReplyNoteBecomesExample(t *testing.T) {
	svc, _, p, item := assistantWithItem(t)
	if _, err := svc.AddNote(p, item.ID, "такое всегда игнорируй"); err != nil {
		t.Fatal(err)
	}
	ex, err := svc.Store().Examples(p.Name, 5)
	if err != nil || len(ex) != 1 || ex[0].Note != "такое всегда игнорируй" || ex[0].Sender != "dana@acme.test" {
		t.Fatalf("examples = %+v, %v", ex, err)
	}
}

func TestRecategorizeBulkBackToInbox(t *testing.T) {
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	news := msg("n1", "Bank <alerts@bank.test>", "Statement", "Your statement", now)
	news.ListUnsubscribe = "<https://bank.test/u>"
	mb := newFakeMailbox(thread("news", news))
	svc := newTestService(t, assistantConfig(), mb, &fakeLLM{}, now)
	p := svc.Profiles()[0]
	d, err := svc.Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	it, err := svc.Recategorize(context.Background(), p, d.Quiet[0].ID, BucketAction, ScopeOnce)
	if err != nil {
		t.Fatal(err)
	}
	if it.Archived {
		t.Error("item still archived")
	}
	log := mb.modifyLog()
	if !strings.Contains(log, "news +Triage/Action,INBOX") {
		t.Fatalf("thread not returned to the inbox:\n%s", log)
	}
	rules, _ := svc.Store().Rules(p.Name)
	if len(rules) != 0 {
		t.Fatalf("once-only correction created rules: %+v", rules)
	}
}

func TestClassifierFailureKeepsWindow(t *testing.T) {
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	mb := newFakeMailbox(thread("t1", msg("m1", "A <a@x.test>", "Hi", "question?", now)))
	failing := CompleterFunc(func(context.Context, string, string) (string, error) {
		return "", context.DeadlineExceeded
	})
	svc := newTestService(t, assistantConfig(), mb, failing, now)
	p := svc.Profiles()[0]
	d, err := svc.Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Errors) == 0 || len(d.Items) != 0 {
		t.Fatalf("digest = %+v", d)
	}
	last, _ := svc.Store().LastRun(p.Name)
	if !last.IsZero() {
		t.Fatal("window advanced after a failed classification")
	}
}

func TestParseThreadJSON(t *testing.T) {
	raw := apiThread{ID: "t1", Messages: []apiMessage{{
		ID: "m1", LabelIDs: []string{"INBOX", "CATEGORY_UPDATES"}, InternalDate: "1759900000000", Snippet: "a &amp; b",
		Payload: apiPart{MimeType: "multipart/alternative", Parts: []apiPart{
			{MimeType: "text/html", Body: struct {
				Data string `json:"data"`
			}{Data: b64("<p>html</p>")}},
			{MimeType: "text/plain", Body: struct {
				Data string `json:"data"`
			}{Data: b64("plain body")}},
		}},
	}}}
	raw.Messages[0].Payload.Headers = append(raw.Messages[0].Payload.Headers,
		struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{"From", "\"Doe, Jane\" <Jane@Example.org>"},
		struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}{"List-Unsubscribe", "<mailto:u@example.org>"},
	)
	th := raw.convert()
	m := th.Messages[0]
	if m.Body != "plain body" || m.Snippet != "a & b" || m.FromAddress() != "jane@example.org" || m.FromName() != "Doe, Jane" || m.ListUnsubscribe == "" || m.ThreadID != "t1" {
		t.Fatalf("message = %+v", m)
	}
	if m.Time.UnixMilli() != 1759900000000 {
		t.Fatalf("time = %v", m.Time)
	}
}

func TestGogArgvKeepsQueryAfterSeparator(t *testing.T) {
	runner := &recordingRunner{}
	client := &GogClient{Binary: "gog", Account: "a@example.com", ReadOnly: true, Run: runner.run}
	if _, err := client.Search(context.Background(), "-in:trash label:x", 20); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(runner.calls[0], " ")
	want := "gmail search --max 20 --json --no-input --account a@example.com -- -in:trash label:x"
	if got != want {
		t.Fatalf("argv = %q\nwant %q", got, want)
	}
}

func TestPrefilterSalesLensIsNarrow(t *testing.T) {
	p := Profile{Taxonomy: TaxonomySales, Language: "en", Account: "sales@example.com"}
	form := thread("f", msg("m", "Website <noreply@site.test>", "New contact form", "I want to license your song", time.Now(), "CATEGORY_UPDATES"))
	if _, _, ok := prefilter(p, form); ok {
		t.Error("sales lens prefiltered a noreply contact form")
	}
	owner := Profile{Taxonomy: TaxonomyOwner, Language: "en", Account: ownerAddr}
	if b, _, ok := prefilter(owner, form); !ok || b != BucketBulk {
		t.Error("owner lens did not prefilter a noreply notification")
	}
	replied := thread("r", msg("m1", ownerAddr, "x", "y", time.Now(), "SENT"), msg("m2", "noreply@site.test", "x", "y", time.Now()))
	if _, _, ok := prefilter(owner, replied); ok {
		t.Error("a thread the owner wrote in was prefiltered")
	}
}

// The sent scan only finds Waiting candidates: an archived conversation that
// the other side answered, or a note to self, is not reported.
func TestSentScanOnlyYieldsWaiting(t *testing.T) {
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	mineOld := msg("s1", ownerAddr, "Question", "?", now.AddDate(0, 0, -10), "SENT")
	mineOld.To = "carol@vendor.test"
	answered := msg("s2", "Carol <carol@vendor.test>", "Re: Question", "done", now.AddDate(0, 0, -8))
	answered.Labels = nil // archived, not in the inbox
	self := msg("s3", ownerAddr, "note", "remember", now.AddDate(0, 0, -9), "SENT")
	self.To = ownerAddr
	mb := &fakeMailbox{threads: map[string]Thread{}}
	mb.add(thread("answered", mineOld, answered))
	mb.add(thread("self", self))
	// Both are found by the sent search only.
	searchSent := func(_ context.Context, q string, _ int) ([]string, error) {
		if strings.HasPrefix(q, "in:sent") {
			return []string{"answered", "self"}, nil
		}
		return nil, nil
	}
	llm := &fakeLLM{}
	clock := func() time.Time { return now }
	svc, err := New(assistantConfig(), Options{
		Store:     testStore(t, clock),
		Completer: llm,
		Mailboxes: func(Profile) (Reader, Writer) { return searchReader{search: searchSent, mb: mb}, mb },
		Now:       clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	d, err := svc.Run(context.Background(), svc.Profiles()[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Items)+len(d.Quiet) != 0 || llm.calls() != 0 {
		t.Fatalf("sent scan produced items: %+v %+v (llm calls %d)", d.Items, d.Quiet, llm.calls())
	}
}

type searchReader struct {
	search func(context.Context, string, int) ([]string, error)
	mb     *fakeMailbox
}

func (r searchReader) Search(ctx context.Context, q string, max int) ([]string, error) {
	return r.search(ctx, q, max)
}

func (r searchReader) Thread(ctx context.Context, id string) (Thread, error) {
	return r.mb.Thread(ctx, id)
}
