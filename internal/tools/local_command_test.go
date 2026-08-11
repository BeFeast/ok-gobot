package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalCommandExecuteFailsClosedWithoutApproval(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	command := fmt.Sprintf("printf executed > %q", marker)

	output, err := (&LocalCommand{}).Execute(context.Background(), command)
	if err == nil {
		t.Fatal("expected an error when approval callback is not configured")
	}
	if !strings.Contains(err.Error(), "approval is not configured") {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "" {
		t.Fatalf("unexpected output: %q", output)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("command executed without approval; marker stat error = %v", statErr)
	}
}

func TestLocalCommandExecuteDoesNotRunWhenApprovalDenied(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	command := fmt.Sprintf("printf executed > %q", marker)
	local := &LocalCommand{
		ApprovalFunc: func(got string) (bool, error) {
			if got != command {
				t.Fatalf("approval command = %q, want %q", got, command)
			}
			return false, nil
		},
	}

	output, err := local.Execute(context.Background(), command)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if output != "Command denied by user" {
		t.Fatalf("output = %q, want denial message", output)
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("denied command executed; marker stat error = %v", statErr)
	}
}
