package tools

import (
	"context"
	"strings"
	"testing"
)

func TestCronAddUsageUsesGenericExecExample(t *testing.T) {
	tool := NewCronTool(nil, 0)
	_, err := tool.Execute(context.Background(), "add")
	if err == nil {
		t.Fatal("cron add without arguments unexpectedly succeeded")
	}

	usage := strings.ToLower(err.Error())
	if !strings.Contains(usage, "ssh ops@example.invalid /usr/local/bin/update-service") {
		t.Fatalf("cron add usage does not contain the generic exec example: %q", err)
	}
	privateHost := "shtr" + "udel"
	if strings.Contains(usage, privateHost) {
		t.Fatalf("cron add usage contains a private deployment hostname: %q", err)
	}
}
