package rolejob

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/role"
	jobruntime "ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

const (
	// DefaultTimeout preserves the bounded lifecycle for role jobs whose manifest
	// does not declare max_duration.
	DefaultTimeout = 5 * time.Minute
)

// AgentSubmitter is the RuntimeHub surface needed by role jobs.
type AgentSubmitter interface {
	Submit(agent.RunRequest) <-chan agent.RunEvent
}

// AgentCanceller is implemented by RuntimeHub and lets a waiting role job
// cancel an in-flight agent run when the durable job context is done first.
type AgentCanceller interface {
	Cancel(agent.SessionKey)
}

// Options carries transport/runtime metadata for one role job.
type Options struct {
	SessionKey         string
	DeliverySessionKey string
	Worker             string
	ModelTier          string
	ChatID             int64
	RunSessionKey      string
	Timeout            time.Duration
	MemoryScope        memory.RecallContext
	OnToolEvent        func(agent.ToolEvent)
	OnDelta            func(string)
	OnDeltaReset       func()
}

// BuildTask combines the role manifest prompt with the operator's input.
func BuildTask(m *role.Manifest, input string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("role manifest is required")
	}

	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(m.Prompt))
	if trimmed := strings.TrimSpace(input); trimmed != "" {
		sb.WriteString("\n\nUser input: ")
		sb.WriteString(trimmed)
	}
	return strings.TrimSpace(sb.String()), nil
}

// JobSpec builds the durable metadata for a role run.
func JobSpec(m *role.Manifest, opts Options) (jobruntime.JobSpec, error) {
	if m == nil {
		return jobruntime.JobSpec{}, fmt.Errorf("role manifest is required")
	}

	worker := strings.TrimSpace(opts.Worker)
	if worker == "" {
		worker = strings.TrimSpace(m.Worker)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = m.MaxDuration
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	return jobruntime.JobSpec{
		Kind:               "role",
		Worker:             worker,
		ModelTier:          strings.TrimSpace(opts.ModelTier),
		SessionKey:         strings.TrimSpace(opts.SessionKey),
		DeliverySessionKey: strings.TrimSpace(opts.DeliverySessionKey),
		Description:        fmt.Sprintf("role:%s", m.Name),
		RoleName:           strings.TrimSpace(m.Name),
		Timeout:            timeout,
		MaxToolCalls:       m.MaxToolCalls,
	}, nil
}

// AgentJobRunner returns a durable JobRunner that executes the role through the
// existing agent RuntimeHub path and persists the agent's final content.
func AgentJobRunner(hub AgentSubmitter, m *role.Manifest, input string, opts Options) jobruntime.JobRunner {
	return func(ctx context.Context, job *storage.Job, svc *jobruntime.JobService) (jobruntime.JobRunResult, error) {
		if hub == nil {
			return jobruntime.JobRunResult{}, fmt.Errorf("role agent runtime is required")
		}
		if job == nil {
			return jobruntime.JobRunResult{}, fmt.Errorf("job is required")
		}

		task, err := BuildTask(m, input)
		if err != nil {
			return jobruntime.JobRunResult{}, err
		}

		budget := m.ToDelegationJob()
		runKey := roleRunSessionKey(job, m, opts)
		memScope := roleMemoryScope(job, m, opts)

		if svc != nil {
			_ = svc.AppendEvent(job.JobID, jobruntime.JobEventProgress, fmt.Sprintf("running role %s", m.Name), nil)
		}

		events := hub.Submit(agent.RunRequest{
			SessionKey:   runKey,
			ChatID:       opts.ChatID,
			Content:      task,
			Context:      ctx,
			OnToolEvent:  opts.OnToolEvent,
			OnDelta:      opts.OnDelta,
			OnDeltaReset: opts.OnDeltaReset,
			Job:          &budget,
			MemoryScope:  memScope,
		})

		select {
		case ev, ok := <-events:
			if !ok {
				return jobruntime.JobRunResult{}, fmt.Errorf("role agent runtime ended without a result")
			}
			switch ev.Type {
			case agent.RunEventDone:
				if ev.Result == nil {
					return jobruntime.JobRunResult{}, fmt.Errorf("role agent runtime returned nil result")
				}
				summary := strings.TrimSpace(ev.Result.Message)
				result := jobruntime.JobRunResult{Summary: summary}
				if summary != "" {
					result.Artifacts = []jobruntime.JobArtifactSpec{{
						Name:     "output",
						Type:     "text",
						MimeType: "text/plain",
						Content:  summary,
					}}
				}
				return result, nil
			case agent.RunEventError:
				if ev.Err != nil {
					return jobruntime.JobRunResult{}, ev.Err
				}
				return jobruntime.JobRunResult{}, fmt.Errorf("role agent runtime failed")
			default:
				return jobruntime.JobRunResult{}, fmt.Errorf("unexpected role agent event: %s", ev.Type)
			}
		case <-ctx.Done():
			if canceller, ok := hub.(AgentCanceller); ok {
				canceller.Cancel(runKey)
			}
			return jobruntime.JobRunResult{}, ctx.Err()
		}
	}
}

func roleRunSessionKey(job *storage.Job, m *role.Manifest, opts Options) agent.SessionKey {
	if key := strings.TrimSpace(opts.RunSessionKey); key != "" {
		return agent.SessionKey(key)
	}
	roleName := "role"
	if m != nil && strings.TrimSpace(m.Name) != "" {
		roleName = strings.TrimSpace(m.Name)
	}
	if job != nil && strings.TrimSpace(job.JobID) != "" {
		return agent.SessionKey(fmt.Sprintf("role:%s:%s", roleName, job.JobID))
	}
	if job != nil && strings.TrimSpace(job.SessionKey) != "" {
		return agent.SessionKey(job.SessionKey)
	}
	return agent.SessionKey("role:" + roleName)
}

func roleMemoryScope(job *storage.Job, m *role.Manifest, opts Options) memory.RecallContext {
	scope := opts.MemoryScope
	if scope.ChatID == 0 {
		scope.ChatID = opts.ChatID
	}
	if scope.SessionKey == "" && job != nil {
		scope.SessionKey = job.SessionKey
	}
	if scope.RoleName == "" && m != nil {
		scope.RoleName = m.Name
	}
	if scope.JobID == "" && job != nil {
		scope.JobID = job.JobID
	}
	return scope
}
