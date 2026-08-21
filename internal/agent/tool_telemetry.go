package agent

import (
	"log"
	"strings"
	"time"
)

// Until 2026-08-21 the journal recorded only "[hub] starting run" and
// "run done": which tool ran, how long it took and whether it failed was
// invisible. That blind spot is why a broken browser tool was mistaken three
// times for a CDP-host problem. One line per tool call closes it.
func logToolCall(name string, start time.Time, outLen int, err error) {
	dur := time.Since(start).Milliseconds()
	if err != nil {
		log.Printf("[tool] name=%s dur=%dms ok=false err=%q", name, dur, truncateErr(err.Error()))
		return
	}
	log.Printf("[tool] name=%s dur=%dms ok=true out=%dB", name, dur, outLen)
}

func truncateErr(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	const max = 200
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
