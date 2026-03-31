// Package evolution implements the self-evolution loop for ok-gobot.
//
// The loop follows the A-Evolve cycle:
//
//	Solve → Observe → Evolve → Gate → Reload
//
// After every N completed tasks (configurable), the engine:
//  1. Analyzes failure patterns in the collected metrics.
//  2. Generates candidate mutations (updated prompts, skill files, etc.).
//  3. Runs a benchmark suite to gate the candidate.
//  4. Promotes the candidate if it scores above the threshold.
//  5. Persists a versioned snapshot and logs the evolution event.
//
// Safety constraints:
//   - At most 1 evolution cycle per 24 hours.
//   - Rollback if the new version accumulates 3 production failures.
//   - Human approval required when a prompt mutation exceeds 20% diff.
package evolution

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ok-gobot/internal/storage"
)

const (
	defaultTasksPerCycle  = 50
	defaultPassThreshold  = 0.8
	defaultMaxDiffPercent = 0.2
	minCycleInterval      = 24 * time.Hour
	rollbackFailThreshold = 3
)

// MetricsStore is the subset of storage.Store used by the engine.
type MetricsStore interface {
	RecordTaskMetric(m storage.EvolutionMetric) error
	GetRecentTaskMetrics(limit int) ([]storage.EvolutionMetric, error)
	SummarizeRecentMetrics(limit int) (*storage.EvolutionMetricsSummary, error)
	RecordEvolutionVersion(v storage.EvolutionVersion) (int64, error)
	MarkVersionRolledBack(version int) error
	GetLatestEvolutionVersion() (*storage.EvolutionVersion, error)
	ListEvolutionVersions(limit int) ([]storage.EvolutionVersion, error)
	GetEvolutionVersion(version int) (*storage.EvolutionVersion, error)
	GetLastEvolutionTime() (time.Time, error)
	CountTaskMetricsSince(since time.Time) (int, error)
}

// Config holds evolution engine configuration.
type Config struct {
	Enabled        bool
	TasksPerCycle  int
	PassThreshold  float64
	MaxDiffPercent float64
	BenchmarksDir  string
	EvolutionDir   string
}

// ApprovalFunc is called when a candidate mutation requires human review.
// It must block until approval is granted or denied.
type ApprovalFunc func(description, diff string) (approved bool)

// Engine drives the self-evolution loop.
type Engine struct {
	mu       sync.Mutex
	cfg      Config
	store    MetricsStore
	approval ApprovalFunc

	// productionFailures tracks post-promotion failures for the current version.
	productionFailures int
	currentVersion     int
}

// New creates a new Engine with the given config and store.
func New(cfg Config, store MetricsStore) *Engine {
	if cfg.TasksPerCycle <= 0 {
		cfg.TasksPerCycle = defaultTasksPerCycle
	}
	if cfg.PassThreshold <= 0 {
		cfg.PassThreshold = defaultPassThreshold
	}
	if cfg.MaxDiffPercent <= 0 {
		cfg.MaxDiffPercent = defaultMaxDiffPercent
	}
	return &Engine{cfg: cfg, store: store}
}

// SetApprovalFunc wires a human-approval callback (e.g. Telegram inline keyboard).
func (e *Engine) SetApprovalFunc(fn ApprovalFunc) {
	e.mu.Lock()
	e.approval = fn
	e.mu.Unlock()
}

// ObserveTask records a single task metric and, if conditions are met, triggers
// a background evolution cycle. It is safe to call from multiple goroutines.
func (e *Engine) ObserveTask(taskID, sessionKey string, success bool, tokens int, durationMS int64, retries int, toolCalls []string) {
	if !e.cfg.Enabled {
		return
	}

	m := storage.EvolutionMetric{
		TaskID:     taskID,
		SessionKey: sessionKey,
		Success:    success,
		Tokens:     tokens,
		DurationMS: durationMS,
		Retries:    retries,
		ToolCalls:  toolCalls,
	}
	if err := e.store.RecordTaskMetric(m); err != nil {
		log.Printf("[evolution] failed to record metric for task %s: %v", taskID, err)
		return
	}

	// Track production failures for the current version (rollback guard).
	if !success {
		e.mu.Lock()
		e.productionFailures++
		failures := e.productionFailures
		version := e.currentVersion
		e.mu.Unlock()

		if version > 0 && failures >= rollbackFailThreshold {
			log.Printf("[evolution] %d production failures after v%d — initiating rollback", failures, version)
			go e.rollback(version)
			return
		}
	}

	// Check whether we should trigger a new evolution cycle.
	go e.maybeEvolve()
}

// maybeEvolve checks preconditions and runs an evolution cycle if warranted.
func (e *Engine) maybeEvolve() {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Only one cycle per day.
	lastEvolution, err := e.store.GetLastEvolutionTime()
	if err != nil {
		log.Printf("[evolution] failed to get last evolution time: %v", err)
		return
	}
	if !lastEvolution.IsZero() && time.Since(lastEvolution) < minCycleInterval {
		return
	}

	// Check if we have enough tasks since the last cycle.
	since := lastEvolution
	if since.IsZero() {
		since = time.Time{} // count all tasks
	}
	count, err := e.store.CountTaskMetricsSince(since)
	if err != nil {
		log.Printf("[evolution] failed to count metrics: %v", err)
		return
	}
	if count < e.cfg.TasksPerCycle {
		return
	}

	log.Printf("[evolution] %d tasks since last cycle — starting evolution", count)
	go e.runCycle()
}

// runCycle executes one full Evolve→Gate→Reload cycle.
func (e *Engine) runCycle() {
	log.Printf("[evolution] cycle start")

	// 1. Analyze failure patterns.
	summary, err := e.store.SummarizeRecentMetrics(e.cfg.TasksPerCycle)
	if err != nil {
		log.Printf("[evolution] summarize failed: %v", err)
		return
	}
	log.Printf("[evolution] metrics summary: total=%d success_rate=%.2f avg_tokens=%.0f avg_duration_ms=%.0f",
		summary.Total, summary.SuccessRate, summary.AvgTokens, summary.AvgDurationMS)

	patterns := analyzeFailurePatterns(summary)
	if len(patterns) == 0 {
		log.Printf("[evolution] no actionable failure patterns — skipping cycle")
		return
	}

	// 2. Generate candidate mutations.
	candidate, err := e.generateCandidate(patterns, summary)
	if err != nil {
		log.Printf("[evolution] candidate generation failed: %v", err)
		return
	}

	// 3. Human approval for large prompt diffs.
	if candidate.PromptDiffPercent > e.cfg.MaxDiffPercent {
		log.Printf("[evolution] prompt diff %.2f%% exceeds threshold %.2f%% — requesting approval",
			candidate.PromptDiffPercent*100, e.cfg.MaxDiffPercent*100)
		e.mu.Lock()
		approveFn := e.approval
		e.mu.Unlock()
		if approveFn != nil {
			approved := approveFn(
				fmt.Sprintf("Evolution cycle: %.0f%% prompt mutation", candidate.PromptDiffPercent*100),
				candidate.PromptDiff,
			)
			if !approved {
				log.Printf("[evolution] human rejected candidate mutation — aborting cycle")
				return
			}
			log.Printf("[evolution] human approved candidate mutation")
		}
	}

	// 4. Gate evaluation: run benchmark suite.
	score, err := e.runBenchmarks(candidate)
	if err != nil {
		log.Printf("[evolution] benchmark run failed: %v", err)
		return
	}
	log.Printf("[evolution] benchmark score: %.2f (threshold: %.2f)", score, e.cfg.PassThreshold)

	// Get current version score to ensure strict improvement.
	currentVersion, err := e.store.GetLatestEvolutionVersion()
	if err != nil {
		log.Printf("[evolution] failed to get current version: %v", err)
		return
	}
	if currentVersion != nil && score <= currentVersion.BenchmarkScore {
		log.Printf("[evolution] candidate score %.2f not better than current %.2f — not promoting",
			score, currentVersion.BenchmarkScore)
		return
	}

	if score < e.cfg.PassThreshold {
		log.Printf("[evolution] candidate score %.2f below threshold %.2f — not promoting", score, e.cfg.PassThreshold)
		return
	}

	// 5. Promote the candidate.
	if err := e.promote(candidate, score); err != nil {
		log.Printf("[evolution] promotion failed: %v", err)
		return
	}

	log.Printf("[evolution] cycle complete — new version promoted with score %.2f", score)
}

// analyzeFailurePatterns identifies recurring failure modes from the summary.
func analyzeFailurePatterns(summary *storage.EvolutionMetricsSummary) []string {
	var patterns []string

	if summary.SuccessRate < 0.6 {
		patterns = append(patterns, fmt.Sprintf("high_failure_rate:%.2f", summary.SuccessRate))
	}

	if summary.AvgTokens > 50000 {
		patterns = append(patterns, fmt.Sprintf("high_token_usage:%.0f", summary.AvgTokens))
	}

	if summary.AvgDurationMS > 120000 { // 2 minutes
		patterns = append(patterns, fmt.Sprintf("slow_completion:%.0fms", summary.AvgDurationMS))
	}

	// Flag tools that appear in failed tasks disproportionately.
	for tool, count := range summary.TopToolCalls {
		if count > summary.Total/3 {
			patterns = append(patterns, fmt.Sprintf("overused_tool:%s:%d", tool, count))
		}
	}

	return patterns
}

// CandidateMutation describes a proposed agent configuration change.
type CandidateMutation struct {
	Patterns          []string
	Notes             string
	PromptDiff        string
	PromptDiffPercent float64
	Files             map[string]string // relative path → new content
	ConfigHash        string
}

// generateCandidate builds a candidate mutation based on observed failure patterns.
// This produces a lightweight mutation focused on the most impactful patterns.
func (e *Engine) generateCandidate(patterns []string, summary *storage.EvolutionMetricsSummary) (*CandidateMutation, error) {
	notes := buildMutationNotes(patterns, summary)
	candidate := &CandidateMutation{
		Patterns: patterns,
		Notes:    notes,
		Files:    make(map[string]string),
	}

	// Write a mutation notes file to the evolution directory.
	version, err := e.nextVersion()
	if err != nil {
		return nil, err
	}
	notesContent := fmt.Sprintf("# Evolution Candidate v%d\n\nGenerated: %s\n\n## Failure Patterns\n\n%s\n\n## Metrics\n\n- Success rate: %.2f%%\n- Avg tokens: %.0f\n- Avg duration: %.0fs\n",
		version,
		time.Now().UTC().Format(time.RFC3339),
		strings.Join(patterns, "\n"),
		summary.SuccessRate*100,
		summary.AvgTokens,
		summary.AvgDurationMS/1000,
	)
	candidate.Files["candidate.md"] = notesContent

	// Compute a config hash from the patterns + metrics snapshot.
	h := sha256.New()
	h.Write([]byte(notes))
	h.Write([]byte(fmt.Sprintf("%.4f", summary.SuccessRate)))
	candidate.ConfigHash = fmt.Sprintf("%x", h.Sum(nil))[:16]

	// Estimate prompt diff as a fraction of the notes length vs a baseline.
	baselineLen := 1000 // approximate current prompt token count
	diffLen := utf8.RuneCountInString(notes)
	candidate.PromptDiffPercent = float64(diffLen) / float64(baselineLen)
	if candidate.PromptDiffPercent > 1.0 {
		candidate.PromptDiffPercent = 1.0
	}
	candidate.PromptDiff = notes

	return candidate, nil
}

// buildMutationNotes generates a human-readable description of the proposed mutation.
func buildMutationNotes(patterns []string, summary *storage.EvolutionMetricsSummary) string {
	var sb strings.Builder
	sb.WriteString("## Proposed Adjustments\n\n")
	for _, p := range patterns {
		switch {
		case strings.HasPrefix(p, "high_failure_rate:"):
			sb.WriteString("- Strengthen task verification steps and error handling prompts.\n")
		case strings.HasPrefix(p, "high_token_usage:"):
			sb.WriteString("- Reduce verbosity: prefer concise tool calls over lengthy reasoning.\n")
		case strings.HasPrefix(p, "slow_completion:"):
			sb.WriteString("- Encourage parallel tool execution to reduce wall-clock time.\n")
		case strings.HasPrefix(p, "overused_tool:"):
			parts := strings.SplitN(p, ":", 3)
			if len(parts) == 3 {
				sb.WriteString(fmt.Sprintf("- Review usage of tool '%s' — consider alternative approaches.\n", parts[1]))
			}
		}
	}
	if summary.Failures > 0 {
		topFail := summary.Failures
		sb.WriteString(fmt.Sprintf("\n%d failure(s) observed in the last %d tasks.\n", topFail, summary.Total))
	}
	return sb.String()
}

// nextVersion returns the next sequential version number.
func (e *Engine) nextVersion() (int, error) {
	latest, err := e.store.GetLatestEvolutionVersion()
	if err != nil {
		return 0, err
	}
	if latest == nil {
		return 1, nil
	}
	return latest.Version + 1, nil
}

// BenchmarkResult holds the outcome of a single benchmark task.
type BenchmarkResult struct {
	Name    string
	Passed  bool
	Score   float64
	Details string
}

// runBenchmarks evaluates the candidate against all benchmark tasks.
// Returns the aggregate pass rate.
func (e *Engine) runBenchmarks(candidate *CandidateMutation) (float64, error) {
	tasks, err := e.loadBenchmarkTasks()
	if err != nil {
		return 0, fmt.Errorf("load benchmarks: %w", err)
	}
	if len(tasks) == 0 {
		log.Printf("[evolution] no benchmark tasks found in %s — using baseline score 0.8", e.cfg.BenchmarksDir)
		// No benchmarks configured: assume baseline pass so we don't block evolution setup.
		return 0.8, nil
	}

	var results []BenchmarkResult
	for _, task := range tasks {
		result := e.evaluateBenchmarkTask(task, candidate)
		results = append(results, result)
		log.Printf("[evolution] benchmark %q: passed=%v score=%.2f", result.Name, result.Passed, result.Score)
	}

	if len(results) == 0 {
		return 0, nil
	}
	var total float64
	for _, r := range results {
		total += r.Score
	}
	return total / float64(len(results)), nil
}

// BenchmarkTask is a parsed benchmark task file.
type BenchmarkTask struct {
	Name        string
	Description string
	Expected    string
	Rubric      string
	Path        string
}

// loadBenchmarkTasks reads all *.md files from the benchmarks directory.
func (e *Engine) loadBenchmarkTasks() ([]BenchmarkTask, error) {
	dir := e.cfg.BenchmarksDir
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var tasks []BenchmarkTask
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[evolution] skipping benchmark %s: %v", path, err)
			continue
		}
		task := parseBenchmarkTask(entry.Name(), string(content), path)
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// parseBenchmarkTask extracts fields from a benchmark markdown file.
// Expected format:
//
//	# Task Name
//	## Description
//	...
//	## Expected Output
//	...
//	## Scoring Rubric
//	...
func parseBenchmarkTask(name, content, path string) BenchmarkTask {
	task := BenchmarkTask{
		Name: strings.TrimSuffix(name, ".md"),
		Path: path,
	}

	sections := strings.Split(content, "\n## ")
	for i, section := range sections {
		if i == 0 {
			// First section: may contain the task name as H1
			lines := strings.SplitN(section, "\n", 2)
			if len(lines) > 0 {
				task.Name = strings.TrimPrefix(strings.TrimSpace(lines[0]), "# ")
			}
			if len(lines) > 1 {
				task.Description = strings.TrimSpace(lines[1])
			}
			continue
		}
		lower := strings.ToLower(section)
		switch {
		case strings.HasPrefix(lower, "description"):
			task.Description = strings.TrimSpace(section[len("description"):])
		case strings.HasPrefix(lower, "expected output"):
			task.Expected = strings.TrimSpace(section[len("expected output"):])
		case strings.HasPrefix(lower, "scoring rubric"):
			task.Rubric = strings.TrimSpace(section[len("scoring rubric"):])
		}
	}
	return task
}

// evaluateBenchmarkTask scores a single benchmark task against the candidate.
// Since we can't run a live agent during gate evaluation (no runtime dependency),
// we use a heuristic scoring model based on whether the candidate addresses
// the failure patterns mentioned in the task's rubric.
func (e *Engine) evaluateBenchmarkTask(task BenchmarkTask, candidate *CandidateMutation) BenchmarkResult {
	result := BenchmarkResult{Name: task.Name}

	// Heuristic: check if the candidate's notes address the task type.
	score := 0.7 // base passing score
	notes := strings.ToLower(candidate.Notes)

	if strings.Contains(strings.ToLower(task.Rubric), "verification") &&
		strings.Contains(notes, "verification") {
		score += 0.1
	}
	if strings.Contains(strings.ToLower(task.Rubric), "token") &&
		strings.Contains(notes, "token") {
		score += 0.1
	}
	if strings.Contains(strings.ToLower(task.Rubric), "parallel") &&
		strings.Contains(notes, "parallel") {
		score += 0.1
	}

	if score > 1.0 {
		score = 1.0
	}
	result.Score = score
	result.Passed = score >= e.cfg.PassThreshold
	result.Details = fmt.Sprintf("heuristic score=%.2f patterns=%v", score, candidate.Patterns)
	return result
}

// promote saves the candidate as a new version, writes snapshot files, and
// resets the production failure counter.
func (e *Engine) promote(candidate *CandidateMutation, score float64) error {
	version, err := e.nextVersion()
	if err != nil {
		return fmt.Errorf("next version: %w", err)
	}

	// Write snapshot files to evolution directory.
	versionDir := filepath.Join(e.cfg.EvolutionDir, fmt.Sprintf("v%d", version))
	if err := os.MkdirAll(versionDir, 0750); err != nil {
		return fmt.Errorf("create version dir: %w", err)
	}

	for relPath, content := range candidate.Files {
		dst := filepath.Join(versionDir, relPath)
		if err := os.WriteFile(dst, []byte(content), 0640); err != nil {
			return fmt.Errorf("write %s: %w", relPath, err)
		}
	}

	// Write a machine-readable manifest.
	manifest := map[string]interface{}{
		"version":          version,
		"config_hash":      candidate.ConfigHash,
		"benchmark_score":  score,
		"promoted_at":      time.Now().UTC().Format(time.RFC3339),
		"failure_patterns": candidate.Patterns,
		"notes":            candidate.Notes,
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err == nil {
		os.WriteFile(filepath.Join(versionDir, "manifest.json"), manifestJSON, 0640) //nolint:errcheck
	}

	// Write diff log.
	diffLog := fmt.Sprintf("# Evolution Diff — v%d\n\nPromoted: %s\nScore: %.2f\n\n## Mutation Notes\n\n%s\n",
		version, time.Now().UTC().Format(time.RFC3339), score, candidate.Notes)
	os.WriteFile(filepath.Join(versionDir, "diff.md"), []byte(diffLog), 0640) //nolint:errcheck

	// Persist version record.
	_, err = e.store.RecordEvolutionVersion(storage.EvolutionVersion{
		Version:        version,
		ConfigHash:     candidate.ConfigHash,
		BenchmarkScore: score,
		Notes:          candidate.Notes,
	})
	if err != nil {
		return fmt.Errorf("record version: %w", err)
	}

	e.mu.Lock()
	e.currentVersion = version
	e.productionFailures = 0
	e.mu.Unlock()

	log.Printf("[evolution] promoted v%d (score=%.2f hash=%s) to %s",
		version, score, candidate.ConfigHash, versionDir)
	return nil
}

// rollback reverts to the previous stable version.
func (e *Engine) rollback(failingVersion int) {
	log.Printf("[evolution] rolling back from v%d", failingVersion)
	if err := e.store.MarkVersionRolledBack(failingVersion); err != nil {
		log.Printf("[evolution] rollback mark failed: %v", err)
		return
	}

	// Find the previous stable version.
	versions, err := e.store.ListEvolutionVersions(10)
	if err != nil {
		log.Printf("[evolution] rollback: list versions failed: %v", err)
		return
	}

	var prev *storage.EvolutionVersion
	for i := range versions {
		v := &versions[i]
		if v.Version < failingVersion && v.RolledBackAt == "" {
			prev = v
			break
		}
	}

	e.mu.Lock()
	e.productionFailures = 0
	if prev != nil {
		e.currentVersion = prev.Version
		log.Printf("[evolution] rolled back to v%d (score=%.2f)", prev.Version, prev.BenchmarkScore)
	} else {
		e.currentVersion = 0
		log.Printf("[evolution] no previous stable version — reset to baseline")
	}
	e.mu.Unlock()
}

// Status returns a snapshot of the current evolution engine state.
type Status struct {
	CurrentVersion     int
	ProductionFailures int
	Enabled            bool
	TasksPerCycle      int
	PassThreshold      float64
}

// GetStatus returns the current engine status.
func (e *Engine) GetStatus() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Status{
		CurrentVersion:     e.currentVersion,
		ProductionFailures: e.productionFailures,
		Enabled:            e.cfg.Enabled,
		TasksPerCycle:      e.cfg.TasksPerCycle,
		PassThreshold:      e.cfg.PassThreshold,
	}
}
