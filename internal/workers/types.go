package workers

import "context"

// Artifact describes a durable output emitted by a worker run.
type Artifact struct {
	Name     string
	Kind     string
	URI      string
	Metadata string
}

// RunRequest is the normalized payload sent to an external worker backend.
type RunRequest struct {
	Prompt     string
	Model      string
	WorkingDir string
}

// RunUpdate is a progress event emitted during worker execution.
type RunUpdate struct {
	Kind    string
	Message string
}

// RunResult is the normalized final output from a worker backend.
type RunResult struct {
	Output    string
	Artifacts []Artifact
}

// Adapter runs a single bounded task on an external worker backend.
type Adapter interface {
	Name() string
	Description() string
	Binary() string
	Run(ctx context.Context, req RunRequest, emit func(RunUpdate)) (RunResult, error)
}

// Info describes an adapter exposed to operators.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Binary      string `json:"binary"`
	Default     bool   `json:"default"`
}
