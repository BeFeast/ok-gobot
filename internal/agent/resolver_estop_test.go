package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/delegation"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

type testEmergencyStopProvider struct {
	enabled bool
}

func (p testEmergencyStopProvider) IsEmergencyStopEnabled() (bool, error) {
	return p.enabled, nil
}

type resolverDangerousTool struct {
	name   string
	called int
}

func (t *resolverDangerousTool) Name() string { return t.name }

func (t *resolverDangerousTool) Description() string { return "resolver dangerous tool" }

func (t *resolverDangerousTool) Execute(context.Context, ...string) (string, error) {
	t.called++
	return "ok", nil
}

type noopCronScheduler struct{}

func (noopCronScheduler) AddJob(string, string, int64) (int64, error)          { return 0, nil }
func (noopCronScheduler) AddExecJob(string, string, int64, int) (int64, error) { return 0, nil }
func (noopCronScheduler) RemoveJob(int64) error                                { return nil }
func (noopCronScheduler) ToggleJob(int64, bool) error                          { return nil }
func (noopCronScheduler) ListJobs() ([]storage.CronJob, error)                 { return nil, nil }
func (noopCronScheduler) GetNextRun(int64) (time.Time, error)                  { return time.Time{}, nil }

func TestRunResolverBuildToolRegistry_PreservesEstopForJobToolAllowlist(t *testing.T) {
	t.Parallel()

	base := tools.NewRegistryWithEmergencyStop(testEmergencyStopProvider{enabled: true})
	messageTool := &resolverDangerousTool{name: "message"}
	base.Register(messageTool)

	resolver := &RunResolver{ToolRegistry: base}
	reg := resolver.buildToolRegistry(0, &AgentProfile{}, false, &delegation.Job{
		ToolAllowlist: []string{"message"},
	})

	_, err := reg.Execute(context.Background(), "message", "ops", "hello")
	if err == nil {
		t.Fatal("expected estop to block dangerous tool selected by job allowlist")
	}
	if !strings.Contains(err.Error(), "estop is ON") {
		t.Fatalf("unexpected error: %v", err)
	}
	if messageTool.called != 0 {
		t.Fatalf("expected dangerous tool not to execute, called=%d", messageTool.called)
	}
}

func TestRunResolverBuildToolRegistry_PreservesEstopWhenInjectingChatTools(t *testing.T) {
	t.Parallel()

	base := tools.NewRegistryWithEmergencyStop(testEmergencyStopProvider{enabled: true})
	messageTool := &resolverDangerousTool{name: "message"}
	base.Register(messageTool)

	resolver := &RunResolver{
		ToolRegistry: base,
		Scheduler:    noopCronScheduler{},
	}

	reg := resolver.buildToolRegistry(123, &AgentProfile{}, false, nil)
	_, err := reg.Execute(context.Background(), "message", "ops", "hello")
	if err == nil {
		t.Fatal("expected estop to block dangerous tool in chat-specific registry")
	}
	if !strings.Contains(err.Error(), "estop is ON") {
		t.Fatalf("unexpected error: %v", err)
	}
	if messageTool.called != 0 {
		t.Fatalf("expected dangerous tool not to execute, called=%d", messageTool.called)
	}
}
