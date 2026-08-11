package bootstrap

import (
	"strings"
	"testing"
)

func TestDefaultScaffoldContainsNoPrivateDeploymentSamples(t *testing.T) {
	for name, content := range scaffoldTemplates {
		lower := strings.ToLower(content)
		for _, forbidden := range []string{
			"10.10.0.",
			"oleg",
			"god",
			"kossoy",
			"netanya",
			"sap",
			"diabetes",
			"metformin",
			"dr. cohen",
			"₪",
		} {
			if strings.Contains(lower, strings.ToLower(forbidden)) {
				t.Errorf("default file %s contains private sample %q", name, forbidden)
			}
		}
	}
}
