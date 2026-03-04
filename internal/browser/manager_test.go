package browser

import (
	"context"
	"errors"
	"os"
	"testing"
)

func stubInstance(cfg profileConfig, userDataDir string, port int) *profileInstance {
	return &profileInstance{
		name:          cfg.name,
		persistent:    cfg.persistent,
		userDataDir:   userDataDir,
		debugPort:     port,
		allocCtx:      context.Background(),
		allocCancel:   func() {},
		browserCtx:    context.Background(),
		browserCancel: func() {},
	}
}

func TestManagerLazyLaunchPerProfile(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	launches := map[string]int{
		ProfileOpenclaw:  0,
		ProfileEphemeral: 0,
	}

	m.launchFn = func(cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		launches[cfg.name]++
		return stubInstance(cfg, userDataDir, debugPort), nil
	}
	m.healthFn = func(port int) error { return nil }

	if m.IsRunning() {
		t.Fatalf("expected manager to be idle before first use")
	}

	_, cancel, err := m.NewTabForProfile(ProfileOpenclaw)
	if err != nil {
		t.Fatalf("NewTabForProfile(openclaw) failed: %v", err)
	}
	cancel()

	if launches[ProfileOpenclaw] != 1 {
		t.Fatalf("expected 1 openclaw launch, got %d", launches[ProfileOpenclaw])
	}

	_, cancel, err = m.NewTabForProfile(ProfileOpenclaw)
	if err != nil {
		t.Fatalf("NewTabForProfile(openclaw) second call failed: %v", err)
	}
	cancel()

	if launches[ProfileOpenclaw] != 1 {
		t.Fatalf("expected openclaw to remain on 1 launch, got %d", launches[ProfileOpenclaw])
	}

	_, cancel, err = m.NewTabForProfile(ProfileEphemeral)
	if err != nil {
		t.Fatalf("NewTabForProfile(ephemeral) failed: %v", err)
	}
	cancel()

	if launches[ProfileEphemeral] != 1 {
		t.Fatalf("expected 1 ephemeral launch, got %d", launches[ProfileEphemeral])
	}
}

func TestManagerAutoRestartWhenHealthCheckFails(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	launches := 0
	nextPort := 19000
	firstPort := 0
	failFirstPort := false

	m.launchFn = func(cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		launches++
		assignedPort := nextPort
		nextPort++
		if launches == 1 {
			firstPort = assignedPort
		}
		return stubInstance(cfg, userDataDir, assignedPort), nil
	}
	m.healthFn = func(port int) error {
		if failFirstPort && port == firstPort {
			return errors.New("cdp endpoint is down")
		}
		return nil
	}

	_, cancel, err := m.NewTabForProfile(ProfileOpenclaw)
	if err != nil {
		t.Fatalf("first NewTabForProfile(openclaw) failed: %v", err)
	}
	cancel()

	failFirstPort = true

	_, cancel, err = m.NewTabForProfile(ProfileOpenclaw)
	if err != nil {
		t.Fatalf("second NewTabForProfile(openclaw) failed: %v", err)
	}
	cancel()

	if launches != 2 {
		t.Fatalf("expected openclaw restart to relaunch browser once; got launches=%d", launches)
	}
}

func TestManagerEphemeralProfileRemovesDataDirOnStop(t *testing.T) {
	m := newManager(t.TempDir(), false)

	var ephemeralDir string
	m.launchFn = func(cfg profileConfig, userDataDir string, debugPort int) (*profileInstance, error) {
		if cfg.name == ProfileEphemeral {
			ephemeralDir = userDataDir
		}
		return stubInstance(cfg, userDataDir, debugPort), nil
	}
	m.healthFn = func(port int) error { return nil }

	if err := m.StartProfile(ProfileEphemeral); err != nil {
		t.Fatalf("StartProfile(ephemeral) failed: %v", err)
	}
	if ephemeralDir == "" {
		t.Fatalf("expected ephemeral userDataDir to be captured")
	}
	if _, err := os.Stat(ephemeralDir); err != nil {
		t.Fatalf("expected ephemeral directory to exist before stop: %v", err)
	}

	m.StopProfile(ProfileEphemeral)

	if _, err := os.Stat(ephemeralDir); !os.IsNotExist(err) {
		t.Fatalf("expected ephemeral directory to be removed on stop, got err=%v", err)
	}
}

func TestManagerStartProfileRejectsUnknownProfile(t *testing.T) {
	m := newManager(t.TempDir(), false)
	defer m.Stop()

	if err := m.StartProfile("unknown"); err == nil {
		t.Fatalf("expected unknown profile error")
	}
}
