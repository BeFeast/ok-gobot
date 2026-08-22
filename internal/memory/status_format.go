package memory

import (
	"fmt"
	"strings"
)

// FormatStatusCLI renders memory health for terminal output.
func FormatStatusCLI(status IndexStatus) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Memory: %s\n", status.State))
	sb.WriteString(fmt.Sprintf("Enabled: %v\n", status.Enabled))
	sb.WriteString(fmt.Sprintf("Backend: %s\n", valueOr(status.BackendType, "unknown")))
	sb.WriteString(fmt.Sprintf("Root: %s\n", valueOr(status.RootPath, "not configured")))
	sb.WriteString(fmt.Sprintf("Watcher: %s\n", valueOr(status.WatcherState, WatcherStateUnknown)))
	sb.WriteString(fmt.Sprintf("Sources: %d\n", status.SourceCount))
	sb.WriteString(fmt.Sprintf("Chunks: %d\n", status.ChunkCount))
	if status.LexicalIndex != "" {
		sb.WriteString(fmt.Sprintf("Lexical index: %s\n", status.LexicalIndex))
	}
	if status.LastIndexedAt != "" {
		sb.WriteString(fmt.Sprintf("Last indexed: %s\n", status.LastIndexedAt))
	} else {
		sb.WriteString("Last indexed: never\n")
	}
	if len(status.ExtraPaths) > 0 {
		sb.WriteString(fmt.Sprintf("Extra paths: %s\n", strings.Join(status.ExtraPaths, ", ")))
	} else {
		sb.WriteString("Extra paths: none configured\n")
	}
	sb.WriteString(fmt.Sprintf("QMD: %s\n", valueOr(status.QMDStatus, "disabled")))
	if status.Stale {
		sb.WriteString("Stale: true\n")
	}
	if status.LastError != "" {
		sb.WriteString(fmt.Sprintf("Last error: %s\n", status.LastError))
	}
	if status.Action != "" {
		sb.WriteString(fmt.Sprintf("Action: %s\n", status.Action))
	}
	return sb.String()
}

// FormatStatusTelegram renders memory health in compact Telegram-safe text.
func FormatStatusTelegram(status IndexStatus) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Memory status: %s\n", status.State))
	sb.WriteString(fmt.Sprintf("Enabled: %v | Backend: %s | Watcher: %s\n",
		status.Enabled,
		valueOr(status.BackendType, "unknown"),
		valueOr(status.WatcherState, WatcherStateUnknown),
	))
	sb.WriteString(fmt.Sprintf("Indexed: %d source(s), %d chunk(s)\n", status.SourceCount, status.ChunkCount))
	if status.LexicalIndex != "" && status.LexicalIndex != LexicalIndexFTS5 {
		sb.WriteString(fmt.Sprintf("Lexical index: %s\n", status.LexicalIndex))
	}
	if status.LastIndexedAt != "" {
		sb.WriteString(fmt.Sprintf("Last indexed: %s\n", status.LastIndexedAt))
	} else {
		sb.WriteString("Last indexed: never\n")
	}
	if len(status.ExtraPaths) > 0 {
		sb.WriteString(fmt.Sprintf("Extra paths: %s\n", strings.Join(status.ExtraPaths, ", ")))
	}
	if status.QMDStatus != "" {
		sb.WriteString(fmt.Sprintf("QMD: %s\n", status.QMDStatus))
	}
	if status.Stale {
		sb.WriteString("Stale: true\n")
	}
	if status.LastError != "" {
		sb.WriteString(fmt.Sprintf("Last error: %s\n", status.LastError))
	}
	if status.Action != "" {
		sb.WriteString(fmt.Sprintf("Action: %s\n", status.Action))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
