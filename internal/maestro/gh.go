package maestro

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ok-gobot/internal/prhygiene"
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

func (c *GHClient) ListOpenPullRequests(ctx context.Context, repo string, limit int) ([]prhygiene.PullRequest, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	args := []string{
		"pr", "list",
		"--state", "open",
		"--limit", strconv.Itoa(limit),
		"--json", "number,title,state,isDraft,createdAt,updatedAt,mergeStateStatus,reviewDecision,statusCheckRollup,latestReviews,comments,url",
	}
	if strings.TrimSpace(repo) != "" {
		args = append(args, "--repo", repo)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	prs, err := parsePullRequests(out)
	if err != nil {
		return nil, err
	}
	return prs, nil
}

func (c *GHClient) ListOpenPullRequestBlockers(ctx context.Context, repo string, limit int, opts prhygiene.Options) ([]prhygiene.Blocker, error) {
	prs, err := c.ListOpenPullRequests(ctx, repo, limit)
	if err != nil {
		return nil, err
	}
	return prhygiene.DiagnoseAll(prs, opts), nil
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

func parsePullRequests(out []byte) ([]prhygiene.PullRequest, error) {
	var raw []struct {
		Number           int               `json:"number"`
		Title            string            `json:"title"`
		State            string            `json:"state"`
		URL              string            `json:"url"`
		IsDraft          bool              `json:"isDraft"`
		MergeStateStatus string            `json:"mergeStateStatus"`
		ReviewDecision   string            `json:"reviewDecision"`
		CreatedAt        time.Time         `json:"createdAt"`
		UpdatedAt        time.Time         `json:"updatedAt"`
		StatusRollup     []json.RawMessage `json:"statusCheckRollup"`
		LatestReviews    []struct {
			State  string `json:"state"`
			Body   string `json:"body"`
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"latestReviews"`
		Comments []struct {
			Body   string `json:"body"`
			Author struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse gh pr list: %w", err)
	}

	prs := make([]prhygiene.PullRequest, 0, len(raw))
	for _, item := range raw {
		pr := prhygiene.PullRequest{
			Number:         item.Number,
			Title:          item.Title,
			State:          item.State,
			URL:            item.URL,
			Draft:          item.IsDraft,
			MergeState:     item.MergeStateStatus,
			ReviewDecision: item.ReviewDecision,
			CreatedAt:      item.CreatedAt,
			UpdatedAt:      item.UpdatedAt,
			Checks:         parseStatusRollup(item.StatusRollup),
		}
		for _, review := range item.LatestReviews {
			pr.Reviews = append(pr.Reviews, prhygiene.Review{
				Author: review.Author.Login,
				State:  review.State,
				Body:   review.Body,
			})
		}
		for _, comment := range item.Comments {
			pr.Comments = append(pr.Comments, prhygiene.Comment{
				Author: comment.Author.Login,
				Body:   comment.Body,
			})
		}
		prs = append(prs, pr)
	}
	return prs, nil
}

func parseStatusRollup(raw []json.RawMessage) []prhygiene.Check {
	checks := make([]prhygiene.Check, 0, len(raw))
	for _, item := range raw {
		var fields map[string]interface{}
		if err := json.Unmarshal(item, &fields); err != nil {
			continue
		}
		check := prhygiene.Check{
			Name:         stringField(fields, "name"),
			WorkflowName: stringField(fields, "workflowName"),
			Status:       stringField(fields, "status"),
			Conclusion:   stringField(fields, "conclusion"),
		}
		if check.Name == "" {
			check.Name = stringField(fields, "context")
		}
		if check.Conclusion == "" {
			check.Conclusion = stringField(fields, "state")
		}
		if check.Status == "" {
			check.Status = stringField(fields, "state")
		}
		checks = append(checks, check)
	}
	return checks
}

func stringField(fields map[string]interface{}, key string) string {
	value, _ := fields[key].(string)
	return value
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
