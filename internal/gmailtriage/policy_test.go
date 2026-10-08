package gmailtriage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingRunner fakes the gog binary and records every argv it was asked to run.
type recordingRunner struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *recordingRunner) run(_ context.Context, _ string, args []string, _ []string, _ string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	r.mu.Unlock()
	path := strings.Join(commandPath(args), " ")
	switch path {
	case "gmail search":
		return []byte(`{"threads":[{"id":"t1"}],"nextPageToken":""}`), nil
	case "gmail thread get":
		return []byte(`{"thread":{"id":"t1","messages":[{"id":"m1","threadId":"t1","labelIds":["INBOX"],"internalDate":"1759900000000",
			"payload":{"mimeType":"text/plain","headers":[{"name":"From","value":"Fan <fan@example.org>"},{"name":"Subject","value":"ur music saved my life"}],
			"body":{"data":"` + b64("thank you so much") + `"}}}]}}`), nil
	case "gmail labels list":
		return []byte(`{"labels":[]}`), nil
	}
	return []byte(`{}`), nil
}

func (r *recordingRunner) mutatingCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		if writeCommands[strings.Join(commandPath(c), " ")] {
			out = append(out, strings.Join(c, " "))
		}
	}
	return out
}

func TestCheckCommandAllowlist(t *testing.T) {
	cases := []struct {
		args     []string
		readOnly bool
		ok       bool
	}{
		{[]string{"gmail", "search", "--max", "5", "--", "in:inbox"}, true, true},
		{[]string{"gmail", "thread", "get", "t1"}, true, true},
		{[]string{"gmail", "labels", "list"}, true, true},
		{[]string{"gmail", "thread", "modify", "t1", "--add", "X"}, true, false},
		{[]string{"gmail", "labels", "create", "--", "X"}, true, false},
		{[]string{"gmail", "send", "--to", "a@b.c"}, true, false},
		{[]string{"gmail", "thread", "modify", "t1", "--add", "X"}, false, true},
		{[]string{"gmail", "send", "--to", "a@b.c"}, false, true},
		// Never used by triage, so refused for every profile.
		{[]string{"gmail", "batch", "delete", "m1"}, false, false},
		{[]string{"gmail", "labels", "delete", "X"}, false, false},
		{[]string{"gmail", "drafts", "send", "d1"}, false, false},
		{[]string{"send", "--to", "a@b.c"}, false, false},
		{[]string{"drive", "ls"}, true, false},
	}
	for _, c := range cases {
		err := checkCommand(c.readOnly, c.args)
		if (err == nil) != c.ok {
			t.Errorf("checkCommand(readOnly=%v, %v) = %v, want ok=%v", c.readOnly, c.args, err, c.ok)
		}
		if err != nil && !errors.Is(err, ErrForbidden) {
			t.Errorf("checkCommand error %v does not wrap ErrForbidden", err)
		}
	}
}

func TestReadOnlyGogClientRefusesMutatingCommandsBeforeExec(t *testing.T) {
	runner := &recordingRunner{}
	client := &GogClient{Account: "sales@example.com", ReadOnly: true, Run: runner.run}
	ctx := context.Background()

	if err := client.ModifyThread(ctx, "t1", []string{"X"}, nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("ModifyThread = %v, want ErrForbidden", err)
	}
	if err := client.TrashThread(ctx, "t1"); !errors.Is(err, ErrForbidden) {
		t.Errorf("TrashThread = %v, want ErrForbidden", err)
	}
	if err := client.SendReply(ctx, Reply{ThreadID: "t1", ReplyToMessageID: "m1", To: "a@example.org", Subject: "Re: x", Body: "hi"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("SendReply = %v, want ErrForbidden", err)
	}
	if err := client.EnsureLabels(ctx, []string{"Triage/Sales"}); !errors.Is(err, ErrForbidden) {
		t.Errorf("EnsureLabels = %v, want ErrForbidden", err)
	}
	if got := runner.mutatingCalls(); len(got) != 0 {
		t.Fatalf("read-only client executed mutating gog commands: %v", got)
	}
}

// A read-only profile goes through a whole run, a correction and every
// action entry point without a single mutating gog invocation, even when the
// mailbox factory hands it a writer.
func TestReadOnlyProfileNeverReachesMutatingEndpoint(t *testing.T) {
	runner := &recordingRunner{}
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	llm := &fakeLLM{verdicts: map[string]llmVerdict{"t1": {Bucket: BucketIgnore, Why: "fan mail"}}}
	svc, err := New(readOnlyConfig(), Options{
		Store:     testStore(t, clock),
		Completer: llm,
		Mailboxes: func(p Profile) (Reader, Writer) {
			// Deliberately unsafe factory: the policy must still drop the writer.
			client := &GogClient{Account: p.Account, ReadOnly: false, Run: runner.run}
			return client, client
		},
		Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	p := svc.Profiles()[0]
	ctx := context.Background()

	digest, err := svc.Run(ctx, p)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(digest.Quiet) != 1 || digest.Quiet[0].Bucket != BucketIgnore || digest.Quiet[0].Archived {
		t.Fatalf("digest quiet = %+v, want one unarchived ignore item", digest.Quiet)
	}
	item := digest.Quiet[0]
	if _, err := svc.Recategorize(ctx, p, item.ID, BucketSales, ScopeSender); err != nil {
		t.Fatalf("Recategorize: %v", err)
	}
	if _, err := svc.NewAction(p, item, ActionTrash); !errors.Is(err, ErrForbidden) {
		t.Errorf("NewAction(trash) = %v, want ErrForbidden", err)
	}
	// Even a forged action row cannot be confirmed for a read-only profile.
	forged, err := svc.store.CreateAction(p.Name, item.ID, ActionTrash, p.ChatID, actionFingerprint(ActionTrash, item))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Confirm(ctx, forged.ID, p.ChatID); !errors.Is(err, ErrForbidden) {
		t.Errorf("Confirm on read-only profile = %v, want ErrForbidden", err)
	}
	mb := svc.mailboxes[p.Name]
	if err := mb.trash(ctx, &confirmation{action: forged}, item); !errors.Is(err, ErrForbidden) {
		t.Errorf("policy trash = %v, want ErrForbidden", err)
	}
	if err := mb.organize(ctx, item.ThreadID, []string{"X"}, nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("policy organize = %v, want ErrForbidden", err)
	}
	if got := runner.mutatingCalls(); len(got) != 0 {
		t.Fatalf("read-only profile reached mutating gog commands: %v", got)
	}
	if !strings.Contains(strings.Join(llm.users, ""), "<untrusted_emails>") {
		t.Fatal("email content was not wrapped as untrusted input")
	}
}

func assistantWithItem(t *testing.T) (*Service, *fakeMailbox, Profile, Item) {
	t.Helper()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	mb := newFakeMailbox(thread("t1", msg("m1", "Dana <dana@acme.test>", "Contract", "Can you confirm by Friday?", now.Add(-time.Hour))))
	llm := &fakeLLM{verdicts: map[string]llmVerdict{"t1": {Bucket: BucketReply, Why: "asks to confirm", Draft: "Confirmed, thanks."}}}
	svc := newTestService(t, assistantConfig(), mb, llm, now)
	p := svc.Profiles()[0]
	digest, err := svc.Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest.Items) != 1 {
		t.Fatalf("digest items = %+v", digest.Items)
	}
	return svc, mb, p, digest.Items[0]
}

func TestAssistantCannotTrashOrSendWithoutConfirmedCallback(t *testing.T) {
	svc, mb, p, item := assistantWithItem(t)
	ctx := context.Background()
	pm := svc.mailboxes[p.Name]

	// No confirmation at all.
	if err := pm.trash(ctx, nil, item); !errors.Is(err, ErrForbidden) {
		t.Errorf("trash without confirmation = %v", err)
	}
	if err := pm.send(ctx, nil, item, Reply{ThreadID: item.ThreadID}); !errors.Is(err, ErrForbidden) {
		t.Errorf("send without confirmation = %v", err)
	}
	// A confirmation for another kind or item does not transfer.
	wrongKind := &confirmation{action: Action{Profile: p.Name, ItemID: item.ID, Kind: ActionTrash, ChatID: p.ChatID}}
	if err := pm.send(ctx, wrongKind, item, Reply{ThreadID: item.ThreadID}); !errors.Is(err, ErrForbidden) {
		t.Errorf("send with trash confirmation = %v", err)
	}
	otherItem := &confirmation{action: Action{Profile: p.Name, ItemID: item.ID + 1, Kind: ActionTrash, ChatID: p.ChatID}}
	if err := pm.trash(ctx, otherItem, item); !errors.Is(err, ErrForbidden) {
		t.Errorf("trash with another item's confirmation = %v", err)
	}
	// Trash and Send labels cannot sneak through organize.
	if err := pm.organize(ctx, item.ThreadID, []string{"TRASH"}, nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("organize TRASH = %v", err)
	}
	// Unknown or foreign-chat button presses do nothing.
	if _, _, err := svc.Confirm(ctx, "deadbeefdeadbeef", p.ChatID); !errors.Is(err, ErrActionUnavailable) {
		t.Errorf("Confirm(unknown) = %v", err)
	}
	send, err := svc.NewAction(p, item, ActionSend)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Confirm(ctx, send.ID, 999); err == nil {
		t.Error("Confirm from a chat without profile succeeded")
	}
	if len(mb.sent)+len(mb.trashed) != 0 {
		t.Fatalf("mailbox changed before confirmation: sent=%v trashed=%v", mb.sent, mb.trashed)
	}

	// The confirmed button sends exactly once.
	got, _, err := svc.Confirm(ctx, send.ID, p.ChatID)
	if err != nil {
		t.Fatalf("Confirm(send) = %v", err)
	}
	if got.Status != StatusSent || len(mb.sent) != 1 {
		t.Fatalf("after confirm: status=%s sent=%v", got.Status, mb.sent)
	}
	r := mb.sent[0]
	if r.ThreadID != "t1" || r.ReplyToMessageID != "m1" || r.To != "dana@acme.test" || r.Subject != "Re: Contract" || r.Body != "Confirmed, thanks." {
		t.Fatalf("reply = %+v", r)
	}
	if _, _, err := svc.Confirm(ctx, send.ID, p.ChatID); !errors.Is(err, ErrActionUnavailable) {
		t.Errorf("second tap = %v, want ErrActionUnavailable", err)
	}
	if len(mb.sent) != 1 {
		t.Fatalf("double tap sent twice: %v", mb.sent)
	}
}

func TestActionExpiresAndIsSupersededByEdit(t *testing.T) {
	svc, mb, p, item := assistantWithItem(t)
	ctx := context.Background()

	old, err := svc.NewAction(p, item, ActionSend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateDraft(p, item.ID, "New text"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Confirm(ctx, old.ID, p.ChatID); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("button for the old draft = %v, want ErrActionUnavailable", err)
	}

	trash, err := svc.NewAction(p, item, ActionTrash)
	if err != nil {
		t.Fatal(err)
	}
	svc.store.now = func() time.Time { return time.Now().Add(ActionTTL + time.Hour) }
	if _, _, err := svc.Confirm(ctx, trash.ID, p.ChatID); !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("expired trash = %v, want ErrActionUnavailable", err)
	}
	if len(mb.sent)+len(mb.trashed) != 0 {
		t.Fatalf("mailbox changed: sent=%v trashed=%v", mb.sent, mb.trashed)
	}
}

func TestConfirmedTrash(t *testing.T) {
	svc, mb, p, item := assistantWithItem(t)
	a, err := svc.NewAction(p, item, ActionTrash)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := svc.Confirm(context.Background(), a.ID, p.ChatID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusTrashed || len(mb.trashed) != 1 || mb.trashed[0] != "t1" {
		t.Fatalf("status=%s trashed=%v", got.Status, mb.trashed)
	}
}
