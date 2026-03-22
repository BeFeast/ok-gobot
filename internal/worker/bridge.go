package worker

import (
	"context"
	"fmt"

	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

// AdapterJobRunner returns a runtime.JobRunner that delegates to the given
// Adapter with a pre-built Request.  The adapter's normalized Result is
// mapped onto the job's summary and persisted as a text artifact.
func AdapterJobRunner(adapter Adapter, req Request) runtime.JobRunner {
	return func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
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
