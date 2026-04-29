package delegation

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWithDefaults(t *testing.T) {
	t.Parallel()

	j := Job{}.WithDefaults()
	if j.MaxToolCalls != DefaultMaxToolCalls {
		t.Errorf("MaxToolCalls = %d, want %d", j.MaxToolCalls, DefaultMaxToolCalls)
	}
	if j.MaxDuration != DefaultMaxDuration {
		t.Errorf("MaxDuration = %v, want %v", j.MaxDuration, DefaultMaxDuration)
	}
	if j.OutputFormat != OutputFormatMarkdown {
		t.Errorf("OutputFormat = %q, want %q", j.OutputFormat, OutputFormatMarkdown)
	}
	if j.MemoryPolicy != MemoryPolicyReadOnly {
		t.Errorf("MemoryPolicy = %q, want %q", j.MemoryPolicy, MemoryPolicyReadOnly)
	}
}

func TestWithDefaultsPreservesExplicitValues(t *testing.T) {
	t.Parallel()

	j := Job{
		MaxToolCalls: 10,
		MaxDuration:  5 * time.Minute,
		MaxTokens:    1000,
		MaxCostUSD:   0.50,
		OutputFormat: "json",
		MemoryPolicy: "allow_writes",
		Model:        "gpt-4",
	}.WithDefaults()

	if j.MaxToolCalls != 10 {
		t.Errorf("MaxToolCalls = %d, want 10", j.MaxToolCalls)
	}
	if j.MaxDuration != 5*time.Minute {
		t.Errorf("MaxDuration = %v, want 5m", j.MaxDuration)
	}
	if j.MaxTokens != 1000 {
		t.Errorf("MaxTokens = %d, want 1000", j.MaxTokens)
	}
	if j.MaxCostUSD != 0.50 {
		t.Errorf("MaxCostUSD = %f, want 0.50", j.MaxCostUSD)
	}
}

func TestContractPromptIncludesTokensAndCost(t *testing.T) {
	t.Parallel()

	j := Job{
		MaxTokens:  5000,
		MaxCostUSD: 1.25,
	}
	prompt := j.ContractPrompt("do something")

	if !strings.Contains(prompt, "max_tokens: 5000") {
		t.Error("expected max_tokens in contract prompt")
	}
	if !strings.Contains(prompt, "max_cost_usd: $1.2500") {
		t.Error("expected max_cost_usd in contract prompt")
	}
}

func TestContractPromptUnlimitedDefaults(t *testing.T) {
	t.Parallel()

	j := Job{}
	prompt := j.ContractPrompt("do something")

	if !strings.Contains(prompt, "max_tokens: unlimited") {
		t.Error("expected unlimited tokens in contract prompt")
	}
	if !strings.Contains(prompt, "max_cost_usd: unlimited") {
		t.Error("expected unlimited cost in contract prompt")
	}
}

func TestCompletionSummaryIncludesBudgets(t *testing.T) {
	t.Parallel()

	j := Job{MaxTokens: 2000, MaxCostUSD: 0.10}
	summary := j.CompletionSummary("result text")

	if !strings.Contains(summary, "2000 tokens") {
		t.Error("expected token budget in completion summary")
	}
	if !strings.Contains(summary, "$0.1000") {
		t.Error("expected cost budget in completion summary")
	}
	if !strings.Contains(summary, "result text") {
		t.Error("expected result in completion summary")
	}
}

func TestMemoryWriteAllowed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		policy string
		want   bool
	}{
		{"", false}, // default normalizes to read_only
		{"read_only", false},
		{"inherit", true},
		{"allow_writes", true},
	}

	for _, tc := range cases {
		j := Job{MemoryPolicy: tc.policy}
		if got := j.MemoryWriteAllowed(); got != tc.want {
			t.Errorf("MemoryWriteAllowed(%q) = %v, want %v", tc.policy, got, tc.want)
		}
	}
}

func TestBudgetExceededError(t *testing.T) {
	t.Parallel()

	err := &BudgetExceededError{
		Reason: LimitToolCalls,
		Report: RunReport{
			Status:        "budget_exceeded",
			LimitReason:   LimitToolCalls,
			ToolCallsUsed: 50,
			ToolCallMax:   50,
		},
	}

	if !strings.Contains(err.Error(), "tool_call_limit") {
		t.Errorf("error message = %q, want to contain tool_call_limit", err.Error())
	}

	var target *BudgetExceededError
	if !errors.As(err, &target) {
		t.Error("errors.As should match *BudgetExceededError")
	}
}

func TestRunReportFormatTelegram(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		report RunReport
		want   string
	}{
		{
			name: "completed",
			report: RunReport{
				Status:        "completed",
				ToolCallsUsed: 5,
				ToolCallMax:   50,
				Summary:       "All done",
			},
			want: "completed",
		},
		{
			name: "budget_exceeded",
			report: RunReport{
				Status:        "budget_exceeded",
				LimitReason:   LimitToolCalls,
				ToolCallsUsed: 50,
				ToolCallMax:   50,
				Summary:       "Hit limit",
			},
			want: "tool_call_limit",
		},
		{
			name: "timed_out",
			report: RunReport{
				Status:        "timed_out",
				ToolCallsUsed: 10,
				ToolCallMax:   50,
			},
			want: "timed out",
		},
		{
			name: "cancelled",
			report: RunReport{
				Status:        "cancelled",
				ToolCallsUsed: 3,
				ToolCallMax:   50,
			},
			want: "cancelled",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.report.FormatTelegram()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("FormatTelegram() = %q, want to contain %q", msg, tc.want)
			}
		})
	}
}

func TestRunReportFormatTelegramTruncation(t *testing.T) {
	t.Parallel()

	report := RunReport{
		Status:        "completed",
		ToolCallsUsed: 1,
		ToolCallMax:   50,
		Summary:       strings.Repeat("x", 2000),
	}

	msg := report.FormatTelegram()
	if !strings.Contains(msg, "...") {
		t.Error("expected truncation marker")
	}
}

func TestNormalizeMemoryPolicy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  string
	}{
		{"", MemoryPolicyReadOnly},
		{"  READ_ONLY ", MemoryPolicyReadOnly},
		{"inherit", MemoryPolicyInherit},
		{"allow_writes", MemoryPolicyAllowWrites},
		{"invalid", MemoryPolicyReadOnly},
	}

	for _, tc := range cases {
		got := NormalizeMemoryPolicy(tc.input)
		if got != tc.want {
			t.Errorf("NormalizeMemoryPolicy(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
