package cli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"ok-gobot/internal/browser"
	"ok-gobot/internal/config"
)

func healthyRemoteCheckResult(endpoint string) browser.RemoteCheckResult {
	return browser.RemoteCheckResult{
		Endpoint:        endpoint,
		BrowserProduct:  "Chrome/140.0.0.0",
		ProtocolVersion: "1.3",
		Completed: []browser.RemoteCheckStage{
			browser.RemoteCheckDiscovery,
			browser.RemoteCheckWebSocket,
			browser.RemoteCheckTarget,
			browser.RemoteCheckEvaluation,
			browser.RemoteCheckCleanup,
		},
	}
}

func TestBrowserStatusRemoteReportsEveryDeepCheckStage(t *testing.T) {
	cfg := &config.Config{Browser: config.BrowserConfig{DebugURL: "http://cdp.example:18803"}}
	checkerCalls := 0
	cmd := newBrowserStatusCommandWithChecker(cfg, func(ctx context.Context, gotCfg *config.Config) (browser.RemoteCheckResult, error) {
		checkerCalls++
		if ctx == nil {
			t.Fatal("checker received nil context")
		}
		if gotCfg != cfg {
			t.Fatal("checker received a different config")
		}
		return healthyRemoteCheckResult(cfg.Browser.DebugURL), nil
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if checkerCalls != 1 {
		t.Fatalf("checker calls = %d, want 1", checkerCalls)
	}
	for _, want := range []string{
		"Remote Browser CDP Status",
		"Endpoint: http://cdp.example:18803",
		"✅ Discovery (/json/version)",
		"✅ Browser WebSocket (Browser.getVersion)",
		"✅ Isolated target creation",
		"✅ Deterministic navigation/evaluation",
		"✅ Target/context cleanup",
		"✅ Remote CDP healthy: Chrome/140.0.0.0 (protocol 1.3)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Chrome installed") || strings.Contains(out.String(), "Profile ready") {
		t.Fatalf("remote status reported local Chrome/profile state:\n%s", out.String())
	}
}

func TestBrowserStatusRemoteDistinguishesFailureStagesAndReturnsError(t *testing.T) {
	tests := []struct {
		stage browser.RemoteCheckStage
		label string
	}{
		{stage: browser.RemoteCheckDiscovery, label: "Discovery (/json/version)"},
		{stage: browser.RemoteCheckWebSocket, label: "Browser WebSocket (Browser.getVersion)"},
		{stage: browser.RemoteCheckTarget, label: "Isolated target creation"},
		{stage: browser.RemoteCheckEvaluation, label: "Deterministic navigation/evaluation"},
		{stage: browser.RemoteCheckCleanup, label: "Target/context cleanup"},
	}
	for _, tt := range tests {
		t.Run(string(tt.stage), func(t *testing.T) {
			cfg := &config.Config{Browser: config.BrowserConfig{DebugURL: "http://cdp.example:18803"}}
			result := browser.RemoteCheckResult{Endpoint: cfg.Browser.DebugURL}
			for _, stage := range []browser.RemoteCheckStage{
				browser.RemoteCheckDiscovery,
				browser.RemoteCheckWebSocket,
				browser.RemoteCheckTarget,
				browser.RemoteCheckEvaluation,
			} {
				if stage == tt.stage {
					break
				}
				result.Completed = append(result.Completed, stage)
			}
			cmd := newBrowserStatusCommandWithChecker(cfg, func(context.Context, *config.Config) (browser.RemoteCheckResult, error) {
				return result, &browser.RemoteCheckError{Stage: tt.stage, Err: errors.New("forced failure")}
			})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)

			err := cmd.Execute()
			if err == nil {
				t.Fatal("Execute() succeeded for unhealthy remote CDP")
			}
			if !strings.Contains(out.String(), "❌ "+tt.label) {
				t.Fatalf("output did not classify %s failure:\n%s", tt.stage, out.String())
			}
			if !strings.Contains(out.String(), "Failure: remote CDP "+string(tt.stage)+" check failed: forced failure") {
				t.Fatalf("output missing typed failure:\n%s", out.String())
			}
		})
	}
}

func TestDoctorRemoteBrowserCheckIsRequiredAndStageAware(t *testing.T) {
	cfg := &config.Config{Browser: config.BrowserConfig{DebugURL: "http://cdp.example:18803"}}

	healthy := checkBrowser(context.Background(), cfg, func(context.Context, *config.Config) (browser.RemoteCheckResult, error) {
		return healthyRemoteCheckResult(cfg.Browser.DebugURL), nil
	})
	if !healthy.required || !healthy.passed || healthy.warning {
		t.Fatalf("healthy remote doctor result = %+v, want required pass", healthy)
	}
	if !strings.Contains(healthy.message, cfg.Browser.DebugURL) || !strings.Contains(healthy.message, "evaluation and cleanup passed") {
		t.Fatalf("healthy remote doctor message = %q", healthy.message)
	}

	for _, stage := range []browser.RemoteCheckStage{
		browser.RemoteCheckDiscovery,
		browser.RemoteCheckWebSocket,
		browser.RemoteCheckTarget,
		browser.RemoteCheckEvaluation,
	} {
		t.Run(string(stage), func(t *testing.T) {
			failed := checkBrowser(context.Background(), cfg, func(context.Context, *config.Config) (browser.RemoteCheckResult, error) {
				return browser.RemoteCheckResult{Endpoint: cfg.Browser.DebugURL}, &browser.RemoteCheckError{
					Stage: stage,
					Err:   errors.New("forced failure"),
				}
			})
			if !failed.required || failed.passed || failed.warning {
				t.Fatalf("failed remote doctor result = %+v, want required failure", failed)
			}
			if !strings.Contains(failed.message, string(stage)+" stage failed: forced failure") {
				t.Fatalf("failed remote doctor message = %q", failed.message)
			}
		})
	}
}

func TestBrowserCommandsKeepLocalBehaviorWithoutDebugURL(t *testing.T) {
	cfg := &config.Config{}
	checkerCalls := 0
	checker := func(context.Context, *config.Config) (browser.RemoteCheckResult, error) {
		checkerCalls++
		return browser.RemoteCheckResult{}, errors.New("remote checker must not run")
	}

	gotDoctor := checkBrowser(context.Background(), cfg, checker)
	wantDoctor := checkChrome()
	if !reflect.DeepEqual(gotDoctor, wantDoctor) {
		t.Fatalf("local doctor result changed:\n got: %+v\nwant: %+v", gotDoctor, wantDoctor)
	}

	cmd := newBrowserStatusCommandWithChecker(cfg, checker)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("local status Execute() error = %v", err)
	}
	if checkerCalls != 0 {
		t.Fatalf("remote checker calls in local mode = %d, want 0", checkerCalls)
	}
	if !strings.HasPrefix(out.String(), "🌐 Chrome Browser Status\n========================\n") {
		t.Fatalf("local status header changed:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Remote Browser CDP Status") || strings.Contains(out.String(), "Endpoint:") {
		t.Fatalf("local status leaked remote output:\n%s", out.String())
	}
}
