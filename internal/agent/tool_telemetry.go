package agent

import (
	"log"
	"strings"
	"time"

	"ok-gobot/internal/tools"
)

// Until 2026-08-21 the journal recorded only "[hub] starting run" and
// "run done": which tool ran, how long it took and whether it failed was
// invisible. That blind spot is why a broken browser tool was mistaken three
// times for a CDP-host problem. One line per tool call closes it.
//
// spawned marks the timeout-spawn branch, where the call returns a placeholder
// notification (a success for the agent loop) while the real work continues as
// a subagent — counting those as failures would distort the very rate this
// telemetry exists to measure.
func logToolCall(name string, start time.Time, outLen int, err error, spawned bool) {
	dur := time.Since(start).Milliseconds()
	switch {
	case spawned:
		log.Printf("[tool] name=%s dur=%dms ok=true spawned=true", name, dur)
	case err == nil:
		log.Printf("[tool] name=%s dur=%dms ok=true out=%dB", name, dur, outLen)
	default:
		// Policy/e-stop blocks are deliberate refusals, not breakage; the agent
		// loop already treats them separately (tools.IsToolDenial).
		if d, ok := tools.IsToolDenial(err); ok {
			log.Printf("[tool] name=%s dur=%dms ok=false denied=true reason=%q", name, dur, truncateErr(d.Reason))
			return
		}
		log.Printf("[tool] name=%s dur=%dms ok=false err=%q", name, dur, truncateErr(err.Error()))
	}
}

func truncateErr(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	const max = 200
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
