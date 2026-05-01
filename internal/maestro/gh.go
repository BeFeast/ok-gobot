package maestro

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// GHClient reads issue state through the GitHub CLI.
type GHClient struct {
	Dir    string
	Binary string
}

func NewGHClient(dir string) *GHClient {
	return &GHClient{Dir: dir, Binary: "gh"}
}

func (c *GHClient) ListOpenIssues(ctx context.Context, repo string, limit int) ([]Issue, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	args := []string{"issue", "list", "--state", "open", "--limit", strconv.Itoa(limit), "--json", "number,title,body,labels,state"}
	if strings.TrimSpace(repo) != "" {
		args = append(args, "--repo", repo)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse gh issue list: %w", err)
	}

	issues := make([]Issue, 0, len(raw))
	for _, item := range raw {
		labels := make([]string, 0, len(item.Labels))
		for _, label := range item.Labels {
			labels = append(labels, label.Name)
		}
		issues = append(issues, Issue{
			Number: item.Number,
			Title:  item.Title,
			Body:   item.Body,
			State:  item.State,
			Labels: labels,
		})
	}
	return issues, nil
}

func (c *GHClient) ResolveDependency(ctx context.Context, defaultRepo string, ref DependencyRef) (DependencyStatus, error) {
	repo := strings.TrimSpace(ref.Repo)
	if repo == "" {
		repo = strings.TrimSpace(defaultRepo)
	}
	status := DependencyStatus{Ref: ref}

	issueState, issueErr := c.issueState(ctx, repo, ref.Number)
	if issueErr == nil {
		status.State = strings.ToLower(issueState)
		if status.Satisfied() {
			return status, nil
		}
	}

	prStatus, prErr := c.prStatus(ctx, repo, ref.Number)
	if prErr == nil {
		return prStatus, nil
	}
	if issueErr != nil {
		return status, issueErr
	}
	return status, nil
}

func (c *GHClient) issueState(ctx context.Context, repo string, number int) (string, error) {
	args := []string{"issue", "view", strconv.Itoa(number), "--json", "state", "--jq", ".state"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (c *GHClient) prStatus(ctx context.Context, repo string, number int) (DependencyStatus, error) {
	args := []string{"pr", "view", strconv.Itoa(number), "--json", "state,isDraft,mergeStateStatus"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return DependencyStatus{}, err
	}
	var raw struct {
		State            string `json:"state"`
		IsDraft          bool   `json:"isDraft"`
		MergeStateStatus string `json:"mergeStateStatus"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return DependencyStatus{}, fmt.Errorf("parse gh pr view: %w", err)
	}
	mergeState := strings.ToUpper(strings.TrimSpace(raw.MergeStateStatus))
	return DependencyStatus{
		Ref:           refFromNumber(repo, number),
		State:         strings.ToLower(strings.TrimSpace(raw.State)),
		IsPullRequest: true,
		MergeReady:    !raw.IsDraft && (mergeState == "CLEAN" || mergeState == "HAS_HOOKS"),
		MergeState:    mergeState,
		Draft:         raw.IsDraft,
	}, nil
}

func refFromNumber(repo string, number int) DependencyRef {
	ref := DependencyRef{Number: number, Raw: fmt.Sprintf("#%d", number)}
	if repo != "" {
		ref.Repo = repo
		ref.Raw = fmt.Sprintf("%s#%d", repo, number)
	}
	return ref
}

func (c *GHClient) run(ctx context.Context, args ...string) ([]byte, error) {
	binary := c.Binary
	if binary == "" {
		binary = "gh"
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	if c.Dir != "" {
		cmd.Dir = c.Dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return nil, err
		}
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return out, nil
}
