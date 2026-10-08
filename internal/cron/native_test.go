package cron

import (
	"testing"
	"time"

	robfig "github.com/robfig/cron/v3"
)

func TestReplaceNativeJobs(t *testing.T) {
	s := &Scheduler{cron: robfig.New(robfig.WithSeconds()), jobs: map[int64]robfig.EntryID{}, native: map[string][]robfig.EntryID{}}
	fn := func() {}
	if err := s.ReplaceNativeJobs("triage:a", []string{"CRON_TZ=Asia/Jerusalem 0 30 8 * * *", "CRON_TZ=Asia/Jerusalem 0 0 19 * * 0,1,2,3,4"}, fn); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceNativeJobs("other", []string{"0 0 * * * *"}, fn); err != nil {
		t.Fatal(err)
	}
	if n := len(s.cron.Entries()); n != 3 {
		t.Fatalf("entries = %d, want 3", n)
	}
	// An invalid spec leaves the previous entries untouched.
	if err := s.ReplaceNativeJobs("triage:a", []string{"0 0 10 * * *", "not a spec"}, fn); err == nil {
		t.Fatal("invalid spec accepted")
	}
	if n := len(s.native["triage:a"]); n != 2 {
		t.Fatalf("group lost entries after a failed replace: %d", n)
	}
	if err := s.ReplaceNativeJobs("triage:a", []string{"0 0 10 * * *"}, fn); err != nil {
		t.Fatal(err)
	}
	if n := len(s.cron.Entries()); n != 2 {
		t.Fatalf("entries after replace = %d, want 2", n)
	}
	s.cron.Start()
	defer s.cron.Stop()
	time.Sleep(10 * time.Millisecond)
	next := s.NativeNextRun("triage:a")
	if next.IsZero() || next.Minute() != 0 || next.Hour() != 10 {
		t.Fatalf("next run = %v", next)
	}
	if err := s.ReplaceNativeJobs("triage:a", nil, fn); err != nil {
		t.Fatal(err)
	}
	if !s.NativeNextRun("triage:a").IsZero() || len(s.cron.Entries()) != 1 {
		t.Fatal("clearing a group left entries behind")
	}
}
