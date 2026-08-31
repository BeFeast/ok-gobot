package app

import (
	"fmt"
	"strings"
)

// describeDegradedStart explains why the process is coming up without a healthy
// primary backend.
//
// Startup used to exit 1 when preflight failed and no fallback models were
// configured. systemd retried, hit its start limit, and stopped: on 2026-08-23
// that produced two outages where the bot was simply gone until a human
// noticed, because a provider blip at boot is indistinguishable from a
// permanent fault at that moment. A process that is up can still answer
// /status, run doctor, and recover on the next per-run health check; a process
// that is not running can do none of those. So preflight no longer decides
// whether the service exists — it only decides what to warn about.
func describeDegradedStart(err error, fallbackModels []string) string {
	if len(fallbackModels) > 0 {
		return fmt.Sprintf(
			"backend preflight still failing (%v) — starting DEGRADED on fallbacks %s",
			err, strings.Join(fallbackModels, ", "),
		)
	}
	return fmt.Sprintf(
		"backend preflight still failing (%v) and no fallback_models are configured — "+
			"starting DEGRADED anyway; replies will fail until the backend recovers, "+
			"and the resolver re-checks health on every run",
		err,
	)
}
