package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// WorktreeStatus represents the current state of a managed worktree.
type WorktreeStatus string

const (
	WorktreeStatusActive WorktreeStatus = "active"
	WorktreeStatusMerged WorktreeStatus = "merged"
	WorktreeStatusClosed WorktreeStatus = "closed"
	WorktreeStatusStale  WorktreeStatus = "stale"
)

// WorktreeEntry represents a tracked git worktree with its associated task.
type WorktreeEntry struct {
	ID        string         `json:"id"`
	Task      string         `json:"task"`
	Branch    string         `json:"branch"`
	Path      string         `json:"path"`
	RepoRoot  string         `json:"repo_root"`
	PRNumber  int            `json:"pr_number,omitempty"`
	PRStatus  string         `json:"pr_status,omitempty"` // "open", "merged", "closed"
	Status    WorktreeStatus `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
}

// WorktreeManager manages git worktrees and persists their state.
type WorktreeManager struct {
	statePath string
}

// NewWorktreeManager creates a manager that stores state at the given path.
func NewWorktreeManager(statePath string) *WorktreeManager {
	return &WorktreeManager{statePath: statePath}
}

// DefaultWorktreeManager returns a manager using the default state path (~/.ok-gobot/worktrees.json).
func DefaultWorktreeManager() (*WorktreeManager, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}
	dir := filepath.Join(homeDir, ".ok-gobot")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}
	return NewWorktreeManager(filepath.Join(dir, "worktrees.json")), nil
}

// List returns all tracked worktree entries.
func (m *WorktreeManager) List() ([]WorktreeEntry, error) {
	data, err := os.ReadFile(m.statePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read state file: %w", err)
	}
	var entries []WorktreeEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse state file: %w", err)
	}
	return entries, nil
}

// save persists the entry list atomically.
func (m *WorktreeManager) save(entries []WorktreeEntry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}
	tmp := m.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("failed to write state file: %w", err)
	}
	return os.Rename(tmp, m.statePath)
}

// Add tracks a new worktree entry.
func (m *WorktreeManager) Add(entry WorktreeEntry) error {
	entries, err := m.List()
	if err != nil {
		return err
	}
	entries = append(entries, entry)
	return m.save(entries)
}

// Update replaces the entry with the same ID.
func (m *WorktreeManager) Update(updated WorktreeEntry) error {
	entries, err := m.List()
	if err != nil {
		return err
	}
	for i, e := range entries {
		if e.ID == updated.ID {
			entries[i] = updated
			return m.save(entries)
		}
	}
	return fmt.Errorf("worktree %q not found", updated.ID)
}

// Remove deletes the entry with the given ID from state.
func (m *WorktreeManager) Remove(id string) error {
	entries, err := m.List()
	if err != nil {
		return err
	}
	filtered := entries[:0]
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, e)
	}
	if !found {
		return fmt.Errorf("worktree %q not found", id)
	}
	return m.save(filtered)
}

// Get returns the entry with the given ID, or nil if not found.
func (m *WorktreeManager) Get(id string) (*WorktreeEntry, error) {
	entries, err := m.List()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.ID == id {
			ec := e
			return &ec, nil
		}
	}
	return nil, nil
}

// CreateWorktree creates a git worktree and branch, registers it in state.
// repoRoot is the root of the main git repository.
// baseDir is the parent directory for new worktrees (default: ~/worktrees/<reponame>).
func (m *WorktreeManager) CreateWorktree(repoRoot, baseDir, task string) (*WorktreeEntry, error) {
	if repoRoot == "" {
		var err error
		repoRoot, err = GitRepoRoot("")
		if err != nil {
			return nil, fmt.Errorf("not inside a git repository: %w", err)
		}
	}

	if baseDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		repoName := filepath.Base(repoRoot)
		baseDir = filepath.Join(homeDir, "worktrees", repoName)
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	id := fmt.Sprintf("wt-%d", time.Now().UnixNano())
	branch := branchName(task, id)
	wtPath := filepath.Join(baseDir, strings.TrimPrefix(id, "wt-"))

	// Create worktree + branch in one step
	out, err := runGit(repoRoot, "worktree", "add", "-b", branch, wtPath, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git worktree add failed: %w\n%s", err, out)
	}

	entry := WorktreeEntry{
		ID:        id,
		Task:      task,
		Branch:    branch,
		Path:      wtPath,
		RepoRoot:  repoRoot,
		Status:    WorktreeStatusActive,
		CreatedAt: time.Now(),
	}

	if err := m.Add(entry); err != nil {
		// Try to clean up the worktree we just created
		_, _ = runGit(repoRoot, "worktree", "remove", "--force", wtPath)
		return nil, fmt.Errorf("failed to save worktree state: %w", err)
	}

	return &entry, nil
}

// DeleteWorktree removes the git worktree, deletes the branch, and removes the entry from state.
func (m *WorktreeManager) DeleteWorktree(id string) error {
	entry, err := m.Get(id)
	if err != nil {
		return err
	}
	if entry == nil {
		return fmt.Errorf("worktree %q not found", id)
	}

	// Remove git worktree
	if _, err := runGit(entry.RepoRoot, "worktree", "remove", "--force", entry.Path); err != nil {
		// If path doesn't exist, proceed anyway
		if _, statErr := os.Stat(entry.Path); !os.IsNotExist(statErr) {
			return fmt.Errorf("git worktree remove failed: %w", err)
		}
	}

	// Prune stale worktree refs
	_, _ = runGit(entry.RepoRoot, "worktree", "prune")

	// Delete branch (ignore error if already deleted or has unmerged changes — best effort)
	_, _ = runGit(entry.RepoRoot, "branch", "-D", entry.Branch)

	return m.Remove(id)
}

// RefreshPRStatus checks GitHub PR status for each active entry and updates state.
// Requires the `gh` CLI to be available. Only entries with open PRs are updated.
func (m *WorktreeManager) RefreshPRStatus() error {
	entries, err := m.List()
	if err != nil {
		return err
	}

	changed := false
	for i, e := range entries {
		if e.Status != WorktreeStatusActive {
			continue
		}
		if e.PRNumber == 0 {
			// Try to find a PR for this branch
			prNum, prStatus, findErr := findPRForBranch(e.RepoRoot, e.Branch)
			if findErr == nil && prNum > 0 {
				entries[i].PRNumber = prNum
				entries[i].PRStatus = prStatus
				changed = true
				if prStatus == "merged" {
					entries[i].Status = WorktreeStatusMerged
				} else if prStatus == "closed" {
					entries[i].Status = WorktreeStatusClosed
				}
			}
		} else {
			prStatus, findErr := getPRStatus(e.RepoRoot, e.PRNumber)
			if findErr == nil {
				entries[i].PRStatus = prStatus
				changed = true
				if prStatus == "merged" {
					entries[i].Status = WorktreeStatusMerged
				} else if prStatus == "closed" {
					entries[i].Status = WorktreeStatusClosed
				}
			}
		}
	}

	if changed {
		return m.save(entries)
	}
	return nil
}

// CleanupMerged removes all worktrees whose PR has been merged or closed.
// Returns the list of deleted entry IDs.
func (m *WorktreeManager) CleanupMerged() ([]string, error) {
	if err := m.RefreshPRStatus(); err != nil {
		return nil, fmt.Errorf("failed to refresh PR status: %w", err)
	}

	entries, err := m.List()
	if err != nil {
		return nil, err
	}

	var deleted []string
	for _, e := range entries {
		if e.Status == WorktreeStatusMerged || e.Status == WorktreeStatusClosed {
			if delErr := m.DeleteWorktree(e.ID); delErr != nil {
				continue // log but don't stop
			}
			deleted = append(deleted, e.ID)
		}
	}
	return deleted, nil
}

// CleanupStale removes all worktrees older than maxAge.
// Returns the list of deleted entry IDs.
func (m *WorktreeManager) CleanupStale(maxAge time.Duration) ([]string, error) {
	entries, err := m.List()
	if err != nil {
		return nil, err
	}

	cutoff := time.Now().Add(-maxAge)
	var deleted []string
	for _, e := range entries {
		if e.CreatedAt.Before(cutoff) {
			if delErr := m.DeleteWorktree(e.ID); delErr != nil {
				continue
			}
			deleted = append(deleted, e.ID)
		}
	}
	return deleted, nil
}

// GitRepoRoot returns the root directory of the git repo containing dir.
// If dir is empty, the current working directory is used.
func GitRepoRoot(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository")
	}
	return strings.TrimSpace(string(out)), nil
}

// runGit runs a git command in the given directory and returns combined output.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// branchName derives a short, url-safe branch name from a task description.
func branchName(task, id string) string {
	slug := strings.ToLower(task)
	re := regexp.MustCompile(`[^a-z0-9]+`)
	slug = re.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		slug = slug[:40]
		slug = strings.TrimRight(slug, "-")
	}
	if slug == "" {
		slug = "task"
	}
	// Append short numeric suffix from id for uniqueness
	suffix := id
	if strings.HasPrefix(suffix, "wt-") {
		suffix = suffix[3:]
	}
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	return fmt.Sprintf("work/%s-%s", slug, suffix)
}

// findPRForBranch uses `gh` to find an open PR for the given branch.
func findPRForBranch(repoRoot, branch string) (int, string, error) {
	cmd := exec.Command("gh", "pr", "list",
		"--head", branch,
		"--json", "number,state",
		"--limit", "1",
	)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return 0, "", err
	}
	var prs []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(out, &prs); err != nil || len(prs) == 0 {
		return 0, "", fmt.Errorf("no PR found")
	}
	return prs[0].Number, strings.ToLower(prs[0].State), nil
}

// getPRStatus returns the current state of a PR by number.
func getPRStatus(repoRoot string, prNumber int) (string, error) {
	cmd := exec.Command("gh", "pr", "view",
		fmt.Sprintf("%d", prNumber),
		"--json", "state",
		"--jq", ".state",
	)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(string(out))), nil
}
