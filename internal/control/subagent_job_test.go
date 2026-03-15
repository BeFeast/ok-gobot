package control

import "testing"

func TestBuildDelegationJob(t *testing.T) {
	job, err := buildDelegationJob(ClientMsg{
		Model:         "model-x",
		Thinking:      "high",
		ToolAllowlist: []string{"browser", "browser"},
		WorkspaceRoot: "/tmp/work",
		MaxToolCalls:  7,
		MaxDuration:   "2m",
		OutputFormat:  "json",
		OutputSchema:  "report_v1",
		MemoryPolicy:  "allow_writes",
	})
	if err != nil {
		t.Fatalf("buildDelegationJob failed: %v", err)
	}

	if job.MaxToolCalls != 7 {
		t.Fatalf("MaxToolCalls = %d, want 7", job.MaxToolCalls)
	}
	if got := job.MaxDuration.String(); got != "2m0s" {
		t.Fatalf("MaxDuration = %q, want %q", got, "2m0s")
	}
	if job.OutputFormat != "json" {
		t.Fatalf("OutputFormat = %q, want json", job.OutputFormat)
	}
	if job.MemoryPolicy != "allow_writes" {
		t.Fatalf("MemoryPolicy = %q, want allow_writes", job.MemoryPolicy)
	}
	if len(job.ToolAllowlist) != 1 || job.ToolAllowlist[0] != "browser" {
		t.Fatalf("ToolAllowlist = %v, want [browser]", job.ToolAllowlist)
	}
}

func TestBuildDelegationJobRejectsInvalidEnums(t *testing.T) {
	if _, err := buildDelegationJob(ClientMsg{OutputFormat: "xml"}); err == nil {
		t.Fatal("expected invalid output format error")
	}
	if _, err := buildDelegationJob(ClientMsg{MemoryPolicy: "yes"}); err == nil {
		t.Fatal("expected invalid memory policy error")
	}
}
