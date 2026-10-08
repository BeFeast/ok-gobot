package gmailtriage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseSchedule(t *testing.T) {
	s, err := ParseSchedule("19:00, 8:30,08:30", "sun-thu", "Asia/Jerusalem")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.Times, ",") != "08:30,19:00" || s.DaysString() != "sun,mon,tue,wed,thu" {
		t.Fatalf("schedule = %+v", s)
	}
	specs := s.CronSpecs()
	if specs[0] != "CRON_TZ=Asia/Jerusalem 0 30 8 * * 0,1,2,3,4" {
		t.Fatalf("spec = %q", specs[0])
	}
	if w, _ := ParseSchedule("10:00", "fri-sun", "UTC"); w.DaysString() != "sun,fri,sat" {
		t.Fatalf("wrapping range = %q", w.DaysString())
	}
	if all, _ := ParseSchedule("10:00", "mon-sun", "UTC"); len(all.Days) != 0 {
		t.Fatalf("all days should collapse to every day: %v", all.Days)
	}
	for _, bad := range [][3]string{{"25:00", "", "UTC"}, {"10", "", "UTC"}, {"", "", "UTC"}, {"10:00", "weekends", "UTC"}, {"10:00", "", "Mars/Base"}} {
		if _, err := ParseSchedule(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("ParseSchedule(%q,%q,%q) accepted", bad[0], bad[1], bad[2])
		}
	}
}

func TestWorkdaysSkipFridaySaturday(t *testing.T) {
	loc := time.UTC
	thu := time.Date(2026, 10, 8, 10, 0, 0, 0, loc)                                // Thursday
	if n := Workdays(thu, time.Date(2026, 10, 11, 9, 0, 0, 0, loc), loc); n != 1 { // Sunday
		t.Fatalf("Thu→Sun = %d, want 1", n)
	}
	if n := Workdays(thu, time.Date(2026, 10, 13, 9, 0, 0, 0, loc), loc); n != 3 { // Tuesday
		t.Fatalf("Thu→Tue = %d, want 3", n)
	}
}

// Schedule and pause set from Telegram live in SQLite, rebuild cron entries
// immediately and survive a restart; the config only supplies the default.
func TestScheduleOverridePauseAndRestart(t *testing.T) {
	db := testDB(t)
	cfg := readOnlyConfig()
	build := func() (*Service, *fakeScheduler) {
		store, err := NewStore(db)
		if err != nil {
			t.Fatal(err)
		}
		mb := newFakeMailbox()
		svc, err := New(cfg, Options{Store: store, Completer: &fakeLLM{}, Mailboxes: func(Profile) (Reader, Writer) { return mb, nil }})
		if err != nil {
			t.Fatal(err)
		}
		sched := &fakeScheduler{}
		if err := svc.StartSchedules(sched, func(Profile) {}); err != nil {
			t.Fatal(err)
		}
		return svc, sched
	}
	svc, sched := build()
	p := svc.Profiles()[0]
	group := scheduleGroup(p)
	if got := sched.get(group); len(got) != 3 || !strings.Contains(got[0], " 30 8 ") {
		t.Fatalf("default specs = %v", got)
	}

	ctx := context.Background()
	out, err := svc.ToolCommand(ctx, p, map[string]string{"action": "schedule", "op": "set", "times": "10:00,19:00", "days": "mon-fri"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "10:00, 19:00") || !strings.Contains(out, "mon,tue,wed,thu,fri") {
		t.Fatalf("set reply = %q", out)
	}
	if got := sched.get(group); len(got) != 2 || got[0] != "CRON_TZ=Asia/Jerusalem 0 0 10 * * 1,2,3,4,5" {
		t.Fatalf("specs after set = %v", got)
	}
	// Changing only the times keeps the weekdays.
	if _, err := svc.ToolCommand(ctx, p, map[string]string{"action": "schedule", "op": "set", "times": "09:00,19:00"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := sched.get(group); len(got) != 2 || got[0] != "CRON_TZ=Asia/Jerusalem 0 0 9 * * 1,2,3,4,5" {
		t.Fatalf("times-only set lost the days: %v", got)
	}
	if _, err := svc.ToolCommand(ctx, p, map[string]string{"action": "schedule", "op": "set", "times": "10:00,19:00", "days": "mon-fri"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ToolCommand(ctx, p, map[string]string{"action": "pause"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := sched.get(group); len(got) != 0 {
		t.Fatalf("paused profile still scheduled: %v", got)
	}

	// Restart: same database, fresh service.
	svc2, sched2 := build()
	if got := sched2.get(group); len(got) != 0 {
		t.Fatalf("pause lost on restart: %v", got)
	}
	if _, err := svc2.ToolCommand(ctx, p, map[string]string{"action": "resume"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := sched2.get(group); len(got) != 2 || !strings.Contains(got[1], " 0 19 ") {
		t.Fatalf("override lost on restart: %v", got)
	}
	out, err = svc2.ToolCommand(ctx, p, map[string]string{"action": "schedule", "op": "show"}, nil)
	if err != nil || !strings.Contains(out, "set from chat") {
		t.Fatalf("show = %q, %v", out, err)
	}
	if _, err := svc2.ToolCommand(ctx, p, map[string]string{"action": "schedule", "op": "reset"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := sched2.get(group); len(got) != 3 {
		t.Fatalf("reset specs = %v", got)
	}
	// An invalid schedule keeps the previous one.
	if _, err := svc2.ToolCommand(ctx, p, map[string]string{"action": "schedule", "op": "set", "times": "7pm"}, nil); err == nil {
		t.Fatal("invalid time accepted")
	}
	if got := sched2.get(group); len(got) != 3 {
		t.Fatalf("invalid set changed specs: %v", got)
	}
}

func TestRulesToolCommand(t *testing.T) {
	now := time.Now()
	svc := newTestService(t, readOnlyConfig(), newFakeMailbox(), &fakeLLM{}, now)
	p := svc.Profiles()[0]
	ctx := context.Background()
	run := func(params map[string]string) (string, error) { return svc.ToolCommand(ctx, p, params, nil) }

	out, err := run(map[string]string{"action": "rules", "op": "add", "value": "bandcamp.com", "bucket": "ignore", "note": "anything from Bandcamp is ignore"})
	if err != nil || !strings.Contains(out, "domain bandcamp.com → ignore") {
		t.Fatalf("add domain = %q, %v", out, err)
	}
	if _, err := run(map[string]string{"action": "rules", "op": "add", "value": "Promo <Deals@Shop.test>", "bucket": "Sales"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(map[string]string{"action": "rules", "op": "add", "scope": "domain", "value": "gmail.com", "bucket": "ignore"}); err == nil {
		t.Fatal("freemail domain rule accepted")
	}
	if _, err := run(map[string]string{"action": "rules", "op": "add", "value": "x.test", "bucket": "waiting"}); err == nil {
		t.Fatal("unknown bucket accepted")
	}
	out, err = run(map[string]string{"action": "rules", "op": "list"})
	if err != nil || !strings.Contains(out, "sender deals@shop.test → sales") || !strings.Contains(out, "#1 domain bandcamp.com") {
		t.Fatalf("list = %q, %v", out, err)
	}
	// Subdomains match a domain rule.
	rules, _ := svc.Store().Rules(p.Name)
	if r, ok := matchRule(rules, "noreply@mail.bandcamp.com"); !ok || r.Bucket != BucketIgnore {
		t.Fatalf("subdomain match = %+v %v", r, ok)
	}
	if _, err := run(map[string]string{"action": "rules", "op": "remove", "id": "#1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(map[string]string{"action": "rules", "op": "remove", "id": "1"}); err == nil {
		t.Fatal("removing a missing rule succeeded")
	}
	if _, err := run(map[string]string{"action": "launch"}); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestConfigValidation(t *testing.T) {
	if p, err := (Config{}).Resolve(); err != nil || p != nil {
		t.Fatalf("disabled config = %v, %v", p, err)
	}
	cfg := assistantConfig()
	cfg.Profiles = append(cfg.Profiles, cfg.Profiles[0])
	if _, err := cfg.Resolve(); err == nil {
		t.Fatal("duplicate profile accepted")
	}
	bad := readOnlyConfig()
	bad.Profiles[0].Mode = "full"
	if _, err := bad.Resolve(); err == nil {
		t.Fatal("unknown mode accepted")
	}
	def := readOnlyConfig()
	def.Profiles[0].Mode = ""
	ps, err := def.Resolve()
	if err != nil || !ps[0].ReadOnly() {
		t.Fatalf("empty mode must default to read_only: %+v %v", ps, err)
	}
	if ps[0].Schedule.Describe("en") != "08:30, 13:30, 19:00 · every day · Asia/Jerusalem" {
		t.Fatalf("default schedule = %q", ps[0].Schedule.Describe("en"))
	}
}

func TestRuPluralAndHeader(t *testing.T) {
	cases := map[int]string{1: "день", 2: "дня", 4: "дня", 5: "дней", 11: "дней", 12: "дней", 21: "день", 22: "дня", 90: "дней"}
	for n, want := range cases {
		if got := ruPlural(n, "день", "дня", "дней"); got != want {
			t.Errorf("ruPlural(%d) = %q, want %q", n, got, want)
		}
	}
	p := Profile{Language: "ru", Taxonomy: TaxonomyOwner, InitialLookbackDays: 2, Schedule: Schedule{Timezone: "UTC"}}
	if h := HeaderText(Digest{Profile: p, FirstRun: true}, time.Now()); !strings.Contains(h, "первый прогон, 2 дня") {
		t.Fatalf("header = %q", h)
	}
}
