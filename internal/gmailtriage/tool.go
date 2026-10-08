package gmailtriage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ToolCommand serves the gmail_triage agent tool for the profile of the
// current chat. None of its actions touch mail: schedule, pause and rules
// only change triage settings, and "run" delivers a digest through runNow.
// Replies are for the agent, which relays them in the user's language.
func (s *Service) ToolCommand(ctx context.Context, p Profile, params map[string]string, runNow func(context.Context) (string, error)) (string, error) {
	get := func(k string) string { return strings.TrimSpace(params[k]) }
	action := strings.ToLower(get("action"))
	op := strings.ToLower(get("op"))
	switch action {
	case "run", "triage":
		if runNow == nil {
			return "", fmt.Errorf("run is not available here")
		}
		return runNow(ctx)
	case "status", "":
		return s.StatusText(p)
	case "pause", "stop":
		if err := s.SetPaused(p, true); err != nil {
			return "", err
		}
		return "Scheduled email digests are paused. /triage still works on demand; resume with action=resume.", nil
	case "resume", "start":
		if err := s.SetPaused(p, false); err != nil {
			return "", err
		}
		sched, _, err := s.Schedule(p)
		if err != nil {
			return "", err
		}
		return "Scheduled email digests resumed: " + sched.Describe("en"), nil
	case "schedule":
		return s.scheduleCommand(p, op, get("times"), get("days"), get("timezone"))
	case "rules", "rule":
		return s.rulesCommand(p, op, get("scope"), get("value"), get("bucket"), get("note"), get("id"))
	}
	return "", fmt.Errorf("unknown action %q (run, status, schedule, pause, resume, rules)", action)
}

func (s *Service) scheduleCommand(p Profile, op, times, days, timezone string) (string, error) {
	switch op {
	case "", "show", "get":
		sched, overridden, err := s.Schedule(p)
		if err != nil {
			return "", err
		}
		paused, err := s.Paused(p)
		if err != nil {
			return "", err
		}
		src := "config default"
		if overridden {
			src = "set from chat"
		}
		out := fmt.Sprintf("Schedule: %s (%s).", sched.Describe("en"), src)
		if paused {
			out += " Scheduled digests are paused."
		}
		return out, nil
	case "set":
		if times == "" {
			current, _, err := s.Schedule(p)
			if err != nil {
				return "", err
			}
			times = strings.Join(current.Times, ",")
		}
		sched, err := s.SetSchedule(p, times, days, timezone)
		if err != nil {
			return "", err
		}
		out := "Schedule saved: " + sched.Describe("en") + "."
		if paused, _ := s.Paused(p); paused {
			out += " Digests are still paused; resume to apply."
		}
		return out, nil
	case "reset", "default":
		sched, err := s.ResetSchedule(p)
		if err != nil {
			return "", err
		}
		return "Schedule reset to the config default: " + sched.Describe("en") + ".", nil
	}
	return "", fmt.Errorf("unknown schedule op %q (show, set, reset)", op)
}

func (s *Service) rulesCommand(p Profile, op, scope, value, bucket, note, id string) (string, error) {
	switch op {
	case "", "list", "show":
		rules, err := s.store.Rules(p.Name)
		if err != nil {
			return "", err
		}
		return RulesText(Profile{Language: "en"}, rules) + "\nBuckets: " + strings.Join(p.ruleBuckets(), ", "), nil
	case "add", "set":
		if value == "" || bucket == "" {
			return "", fmt.Errorf("rules add needs value (address or domain) and bucket")
		}
		if scope == "" {
			scope = ScopeSender
			if !strings.Contains(value, "@") || strings.HasPrefix(value, "@") {
				scope = ScopeDomain
			}
		}
		r, err := s.AddRule(p, scope, value, bucket, note)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Rule #%d saved: %s %s → %s. It applies from the next digest.", r.ID, r.Scope, r.Value, r.Bucket), nil
	case "remove", "delete":
		n, err := strconv.ParseInt(strings.TrimPrefix(id, "#"), 10, 64)
		if err != nil {
			return "", fmt.Errorf("rules remove needs the numeric id from rules list")
		}
		ok, err := s.store.DeleteRule(p.Name, n)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("no rule #%d", n)
		}
		return fmt.Sprintf("Rule #%d removed.", n), nil
	}
	return "", fmt.Errorf("unknown rules op %q (list, add, remove)", op)
}
