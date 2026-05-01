package reliability

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadManifestFile reads a YAML or JSON benchmark manifest from disk.
func LoadManifestFile(path string) (Manifest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Manifest{}, fmt.Errorf("manifest path is required")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest %s: %w", path, err)
	}
	manifest, err := ParseManifest(data)
	if err != nil {
		return Manifest{}, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	return manifest, nil
}

// ParseManifest decodes and validates a benchmark manifest.
func ParseManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ValidateManifest checks the provider-neutral parts of a manifest. Provider
// availability is checked by Runner because providers are supplied at runtime.
func ValidateManifest(manifest Manifest) error {
	if len(manifest.Scenarios) == 0 {
		return fmt.Errorf("manifest must contain at least one scenario")
	}

	seen := make(map[string]bool, len(manifest.Scenarios))
	for i, scenario := range manifest.Scenarios {
		prefix := fmt.Sprintf("scenario %d", i+1)
		if strings.TrimSpace(scenario.ID) == "" {
			return fmt.Errorf("%s: id is required", prefix)
		}
		if seen[scenario.ID] {
			return fmt.Errorf("%s: duplicate id %q", prefix, scenario.ID)
		}
		seen[scenario.ID] = true

		provider := normalizedProvider(scenario.Provider)
		if provider == ProviderFake {
			if err := validateFakeScenario(prefix, scenario.Fake); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateFakeScenario(prefix string, fake FakeScenario) error {
	if len(fake.Events) == 0 {
		return fmt.Errorf("%s: fake.events must contain at least one lifecycle event", prefix)
	}
	if fake.Outcome != "" && !validOutcome(fake.Outcome) {
		return fmt.Errorf("%s: invalid fake.outcome %q", prefix, fake.Outcome)
	}
	if fake.FailureCategory != "" && !validCategory(fake.FailureCategory) {
		return fmt.Errorf("%s: invalid fake.failure_category %q", prefix, fake.FailureCategory)
	}
	if fake.RetryAttempts < 0 {
		return fmt.Errorf("%s: fake.retry_attempts cannot be negative", prefix)
	}
	for i, event := range fake.Events {
		eventPrefix := fmt.Sprintf("%s event %d", prefix, i+1)
		if !validLifecycleState(event.State) {
			return fmt.Errorf("%s: invalid state %q", eventPrefix, event.State)
		}
		if event.Status != "" && !validEventStatus(event.Status) {
			return fmt.Errorf("%s: invalid status %q", eventPrefix, event.Status)
		}
	}
	return nil
}

func normalizedProvider(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ProviderFake
	}
	return provider
}

func validLifecycleState(state LifecycleState) bool {
	for _, required := range RequiredLifecycleStates {
		if state == required {
			return true
		}
	}
	return false
}

func validEventStatus(status EventStatus) bool {
	switch status {
	case EventStatusPassed, EventStatusFailed, EventStatusSkipped, EventStatusInfo:
		return true
	default:
		return false
	}
}

func validOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeMergeReady, OutcomeBlocked, OutcomeSkipped:
		return true
	default:
		return false
	}
}

func validCategory(category FailureCategory) bool {
	switch category {
	case CategoryNone, CategoryAgentFailure, CategoryEnvironmentFailure, CategoryCIFailure, CategoryReviewFailure, CategoryPolicyGatedSkip:
		return true
	default:
		return false
	}
}
