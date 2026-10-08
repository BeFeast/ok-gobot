package gmailtriage

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func testStore(t *testing.T, now func() time.Time) *Store {
	t.Helper()
	s, err := NewStore(testDB(t))
	if err != nil {
		t.Fatal(err)
	}
	if now != nil {
		s.now = now
	}
	return s
}

const ownerAddr = "owner@example.com"

func assistantConfig() Config {
	return Config{Enabled: true, Profiles: []ProfileConfig{{
		Name: "personal", Mode: "assistant", Taxonomy: "owner", Language: "ru",
		Account: ownerAddr, Query: "in:inbox", ChatID: 100,
	}}}
}

func readOnlyConfig() Config {
	return Config{Enabled: true, Profiles: []ProfileConfig{{
		Name: "shared", Mode: "read_only", Taxonomy: "sales", Language: "en",
		Account: "sales@example.com", Query: "label:info", ChatID: 200, InitialLookbackDays: 90,
	}}}
}

// fakeMailbox is an in-memory Reader and Writer that records every call.
type fakeMailbox struct {
	mu       sync.Mutex
	threads  map[string]Thread
	order    []string
	searches []string
	modifies []string
	trashed  []string
	sent     []Reply
	labels   []string
}

func newFakeMailbox(threads ...Thread) *fakeMailbox {
	f := &fakeMailbox{threads: map[string]Thread{}}
	for _, t := range threads {
		f.add(t)
	}
	return f
}

func (f *fakeMailbox) add(t Thread) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.threads[t.ID]; !ok {
		f.order = append(f.order, t.ID)
	}
	f.threads[t.ID] = t
}

func (f *fakeMailbox) Search(_ context.Context, query string, _ int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searches = append(f.searches, query)
	var ids []string
	for _, id := range f.order {
		last := f.threads[id].Last()
		sentQuery := strings.HasPrefix(query, "in:sent")
		if sentQuery == last.HasLabel("SENT") {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (f *fakeMailbox) Thread(_ context.Context, id string) (Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.threads[id]
	if !ok {
		return Thread{}, fmt.Errorf("no thread %s", id)
	}
	return t, nil
}

func (f *fakeMailbox) EnsureLabels(_ context.Context, names []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labels = append(f.labels, names...)
	return nil
}

func (f *fakeMailbox) ModifyThread(_ context.Context, id string, add, remove []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modifies = append(f.modifies, fmt.Sprintf("%s +%s -%s", id, strings.Join(add, ","), strings.Join(remove, ",")))
	return nil
}

func (f *fakeMailbox) TrashThread(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.trashed = append(f.trashed, id)
	return nil
}

func (f *fakeMailbox) SendReply(_ context.Context, r Reply) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, r)
	return nil
}

func (f *fakeMailbox) modifyLog() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.modifies, "\n")
}

// fakeLLM answers with a fixed verdict per thread ID and records prompts.
type fakeLLM struct {
	mu       sync.Mutex
	verdicts map[string]llmVerdict
	systems  []string
	users    []string
}

func (f *fakeLLM) Complete(_ context.Context, system, user string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.systems = append(f.systems, system)
	f.users = append(f.users, user)
	var out []llmVerdict
	for id, v := range f.verdicts {
		if strings.Contains(user, `"id": "`+id+`"`) {
			v.ID = id
			out = append(out, v)
		}
	}
	data, _ := json.Marshal(out)
	return "Here you go:\n" + string(data), nil
}

func (f *fakeLLM) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.users)
}

func msg(id, from, subject, body string, at time.Time, labels ...string) Message {
	return Message{ID: id, From: from, Subject: subject, Body: body, Time: at, Labels: append([]string{"INBOX"}, labels...)}
}

func thread(id string, msgs ...Message) Thread {
	for i := range msgs {
		msgs[i].ThreadID = id
	}
	return Thread{ID: id, Messages: msgs}
}

func newTestService(t *testing.T, cfg Config, mb *fakeMailbox, llm Completer, now time.Time) *Service {
	t.Helper()
	clock := func() time.Time { return now }
	svc, err := New(cfg, Options{
		Store:     testStore(t, clock),
		Completer: llm,
		Mailboxes: func(Profile) (Reader, Writer) { return mb, mb },
		SkillText: func() string { return "SKILL BODY" },
		Now:       clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// fakeScheduler records native cron registrations.
type fakeScheduler struct {
	mu    sync.Mutex
	specs map[string][]string
	fns   map[string]func()
}

func (f *fakeScheduler) ReplaceNativeJobs(group string, specs []string, fn func()) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.specs == nil {
		f.specs = map[string][]string{}
		f.fns = map[string]func(){}
	}
	f.specs[group] = append([]string(nil), specs...)
	f.fns[group] = fn
	return nil
}

func (f *fakeScheduler) NativeNextRun(string) time.Time { return time.Time{} }

func (f *fakeScheduler) get(group string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[group]
}
