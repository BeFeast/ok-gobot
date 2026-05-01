package worker

import (
	"context"
	"fmt"
	"strings"

	"ok-gobot/internal/evidence"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

// AdapterJobRunner returns a runtime.JobRunner that delegates to the given
// Adapter with a pre-built Request.  The adapter's normalized Result is
// mapped onto the job's summary and persisted as a text artifact.
func AdapterJobRunner(adapter Adapter, req Request) runtime.JobRunner {
	return func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
		if report, ok := RunAdapterPreflight(ctx, adapter, req); ok {
			_ = svc.AddArtifact(job.JobID, runtime.JobArtifactSpec{
				Name:     "preflight.json",
				Type:     "preflight_evidence",
				MimeType: "application/json",
				Content:  report.EvidenceJSON(),
			})
			if !report.OK {
				return runtime.JobRunResult{}, runtime.NewPreflightFailure(report.Summary(), report.FailureReasons())
			}
			_ = svc.AppendEvent(job.JobID, runtime.JobEventProgress, "preflight passed", map[string]any{
				"backend": report.Backend,
				"model":   report.Model,
			})
		}

		if strings.TrimSpace(req.Model) != "" || strings.TrimSpace(job.Worker) != "" {
			_ = svc.AppendEvidence(job.JobID, evidence.EventBackendModel, "selected", "selected worker backend/model", map[string]any{
				"backend": job.Worker,
				"model":   req.Model,
			})
		}
		if strings.TrimSpace(req.WorkDir) != "" {
			_ = svc.AppendEvidence(job.JobID, evidence.EventWorkspace, "selected", "selected worktree", map[string]any{
				"worktree_path": req.WorkDir,
			})
		}
		_ = svc.AppendEvent(job.JobID, runtime.JobEventProgress, fmt.Sprintf("running %s task", job.Worker), nil)

		result, err := adapter.Run(ctx, req)
		if err != nil {
			return runtime.JobRunResult{}, err
		}

		return runtime.JobRunResult{
			Summary: result.Content,
			Artifacts: []runtime.JobArtifactSpec{
				{
					Name:     "output",
					Type:     "text",
					MimeType: "text/plain",
					Content:  result.Content,
				},
			},
		}, nil
	}
}
