package gmailtriage

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Schedule is when digests go out: local times on selected weekdays.
type Schedule struct {
	Times    []string       // "HH:MM", sorted, unique
	Days     []time.Weekday // empty means every day
	Timezone string         // IANA name
}

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

// ParseSchedule parses comma-separated HH:MM times, a day list such as
// "sun-thu" or "mon,wed,fri" (empty, "all" or "daily" = every day) and an
// IANA timezone.
func ParseSchedule(times, days, timezone string) (Schedule, error) {
	s := Schedule{Timezone: strings.TrimSpace(timezone)}
	if s.Timezone == "" {
		s.Timezone = DefaultTimezone
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return s, fmt.Errorf("unknown timezone %q", s.Timezone)
	}
	seen := map[string]bool{}
	for _, raw := range splitList(times) {
		t, err := parseClock(raw)
		if err != nil {
			return s, err
		}
		if !seen[t] {
			seen[t] = true
			s.Times = append(s.Times, t)
		}
	}
	if len(s.Times) == 0 {
		return s, errors.New("schedule needs at least one HH:MM time")
	}
	sort.Strings(s.Times)
	d, err := parseDays(days)
	if err != nil {
		return s, err
	}
	s.Days = d
	return s, nil
}

func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' })
}

func parseClock(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	hh, mm, ok := strings.Cut(raw, ":")
	if !ok {
		return "", fmt.Errorf("time %q must be HH:MM", raw)
	}
	h, err1 := strconv.Atoi(hh)
	m, err2 := strconv.Atoi(mm)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 || len(mm) != 2 {
		return "", fmt.Errorf("time %q must be HH:MM", raw)
	}
	return fmt.Sprintf("%02d:%02d", h, m), nil
}

func parseDays(raw string) ([]time.Weekday, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "", "all", "daily", "every day", "everyday", "*":
		return nil, nil
	}
	set := map[time.Weekday]bool{}
	for _, part := range splitList(raw) {
		if from, to, isRange := strings.Cut(part, "-"); isRange {
			a, ok1 := weekdayNames[from]
			b, ok2 := weekdayNames[to]
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("unknown day range %q", part)
			}
			for d := a; ; d = (d + 1) % 7 {
				set[d] = true
				if d == b {
					break
				}
			}
			continue
		}
		d, ok := weekdayNames[part]
		if !ok {
			return nil, fmt.Errorf("unknown day %q (use sun..sat, ranges like sun-thu)", part)
		}
		set[d] = true
	}
	if len(set) == 0 || len(set) == 7 {
		return nil, nil
	}
	out := make([]time.Weekday, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// CronSpecs renders seconds-enabled cron specs for the schedule.
func (s Schedule) CronSpecs() []string {
	dow := "*"
	if len(s.Days) > 0 {
		parts := make([]string, len(s.Days))
		for i, d := range s.Days {
			parts[i] = strconv.Itoa(int(d))
		}
		dow = strings.Join(parts, ",")
	}
	specs := make([]string, 0, len(s.Times))
	for _, t := range s.Times {
		hh, mm, _ := strings.Cut(t, ":")
		h, _ := strconv.Atoi(hh)
		m, _ := strconv.Atoi(mm)
		specs = append(specs, fmt.Sprintf("CRON_TZ=%s 0 %d %d * * %s", s.Timezone, m, h, dow))
	}
	return specs
}

// DaysString renders the day list in the compact sun,mon form ("" = every day).
func (s Schedule) DaysString() string {
	if len(s.Days) == 0 {
		return ""
	}
	parts := make([]string, len(s.Days))
	for i, d := range s.Days {
		parts[i] = strings.ToLower(d.String()[:3])
	}
	return strings.Join(parts, ",")
}

// Describe is a one-line human summary.
func (s Schedule) Describe(lang string) string {
	days := s.DaysString()
	if days == "" {
		days = tr(lang, "every day", "каждый день")
	}
	return fmt.Sprintf("%s · %s · %s", strings.Join(s.Times, ", "), days, s.Timezone)
}

// Workdays counts whole Sun–Thu working days elapsed between from and to in loc.
// The day of from itself does not count.
func Workdays(from, to time.Time, loc *time.Location) int {
	if !to.After(from) {
		return 0
	}
	start := from.In(loc)
	end := to.In(loc)
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	n := 0
	for !day.After(end) {
		if wd := day.Weekday(); wd != time.Friday && wd != time.Saturday {
			n++
		}
		day = day.AddDate(0, 0, 1)
	}
	return n
}

func tr(lang, en, ru string) string {
	if lang == "ru" {
		return ru
	}
	return en
}
