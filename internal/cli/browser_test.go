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
	cmd := newBrowserStatusCommandWithChecker(cfg, func(ctx context.Context, gotCfg *config.Config, profile browser.AccountProfile) (browser.RemoteCheckResult, error) {
		checkerCalls++
		if ctx == nil {
			t.Fatal("checker received nil context")
		}
		if gotCfg != cfg {
			t.Fatal("checker received a different config")
		}
		if profile.Name != "default" || profile.DebugURL != cfg.Browser.DebugURL {
			t.Fatalf("checker received profile %+v", profile)
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
		"Profile: default [default]",
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
			cmd := newBrowserStatusCommandWithChecker(cfg, func(context.Context, *config.Config, browser.AccountProfile) (browser.RemoteCheckResult, error) {
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

	healthyResults := checkBrowser(context.Background(), cfg, func(context.Context, *config.Config, browser.AccountProfile) (browser.RemoteCheckResult, error) {
		return healthyRemoteCheckResult(cfg.Browser.DebugURL), nil
	})
	if len(healthyResults) != 1 {
		t.Fatalf("legacy config produced %d doctor results, want 1", len(healthyResults))
	}
	healthy := healthyResults[0]
	if healthy.name != "Remote browser CDP" {
		t.Fatalf("legacy doctor result name = %q", healthy.name)
	}
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
			failed := checkBrowser(context.Background(), cfg, func(context.Context, *config.Config, browser.AccountProfile) (browser.RemoteCheckResult, error) {
				return browser.RemoteCheckResult{Endpoint: cfg.Browser.DebugURL}, &browser.RemoteCheckError{
					Stage: stage,
					Err:   errors.New("forced failure"),
				}
			})[0]
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
	checker := func(context.Context, *config.Config, browser.AccountProfile) (browser.RemoteCheckResult, error) {
		checkerCalls++
		return browser.RemoteCheckResult{}, errors.New("remote checker must not run")
	}

	gotDoctor := checkBrowser(context.Background(), cfg, checker)
	wantDoctor := []checkResult{checkChrome()}
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

func twoProfileConfig() *config.Config {
	return &config.Config{Browser: config.BrowserConfig{
		DefaultProfile: "work",
		Profiles: map[string]config.BrowserProfileConfig{
			"personal": {Account: "me@personal.example", DebugURL: "http://cdp.example:9221"},
			"work":     {Account: "me@example.com", DebugURL: "http://cdp.example:9224"},
		},
	}}
}

// A dead profile must not hide a healthy one: both blocks are printed, the
// command still fails, and doctor emits one required result per profile.
func TestBrowserStatusAndDoctorReportEveryProfile(t *testing.T) {
	cfg := twoProfileConfig()
	var checked []string
	checker := func(_ context.Context, _ *config.Config, profile browser.AccountProfile) (browser.RemoteCheckResult, error) {
		checked = append(checked, profile.Name)
		if profile.Name == "personal" {
			return browser.RemoteCheckResult{Endpoint: profile.DebugURL}, &browser.RemoteCheckError{
				Stage: browser.RemoteCheckDiscovery,
				Err:   errors.New("connection refused"),
			}
		}
		return healthyRemoteCheckResult(profile.DebugURL), nil
	}

	cmd := newBrowserStatusCommandWithChecker(cfg, checker)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "profile personal") {
		t.Fatalf("Execute() error = %v, want failure naming the personal profile", err)
	}
	if !reflect.DeepEqual(checked, []string{"personal", "work"}) {
		t.Fatalf("checked profiles = %v, want both in name order", checked)
	}
	for _, want := range []string{
		"Profile: personal (me@personal.example)\nEndpoint: http://cdp.example:9221",
		"❌ Discovery (/json/version)",
		"Failure: remote CDP discovery check failed: connection refused",
		"Profile: work (me@example.com) [default]\nEndpoint: http://cdp.example:9224",
		"✅ Remote CDP healthy: Chrome/140.0.0.0 (protocol 1.3)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status output missing %q:\n%s", want, out.String())
		}
	}

	checked = nil
	results := checkBrowser(context.Background(), cfg, checker)
	if len(results) != 2 {
		t.Fatalf("doctor results = %d, want 2:\n%+v", len(results), results)
	}
	if results[0].name != "Remote browser CDP (personal (me@personal.example))" || results[0].passed || !results[0].required {
		t.Fatalf("personal doctor result = %+v", results[0])
	}
	if !strings.Contains(results[0].message, "discovery stage failed: connection refused") {
		t.Fatalf("personal doctor message = %q", results[0].message)
	}
	if results[1].name != "Remote browser CDP (work (me@example.com))" || !results[1].passed || !results[1].required {
		t.Fatalf("work doctor result = %+v", results[1])
	}
}

func TestBrowserStatusRejectsInvalidProfiles(t *testing.T) {
	cfg := twoProfileConfig()
	cfg.Browser.DefaultProfile = "missing"
	cmd := newBrowserStatusCommandWithChecker(cfg, func(context.Context, *config.Config, browser.AccountProfile) (browser.RemoteCheckResult, error) {
		t.Fatal("checker must not run for an invalid profile set")
		return browser.RemoteCheckResult{}, nil
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "default_profile") {
		t.Fatalf("Execute() error = %v, want default_profile validation error", err)
	}
	results := checkBrowser(context.Background(), cfg, nil)
	if len(results) != 1 || results[0].passed || !strings.Contains(results[0].message, "default_profile") {
		t.Fatalf("doctor results for invalid profiles = %+v", results)
	}
}
