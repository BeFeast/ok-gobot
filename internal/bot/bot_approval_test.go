package bot

import (
	"strings"
	"testing"

	"ok-gobot/internal/tools"
)

type approvalTestEmergencyStop struct{}

func (approvalTestEmergencyStop) IsEmergencyStopEnabled() (bool, error) {
	return false, nil
}

func TestInitializeApprovalSystemWiresWrappedLocalCommand(t *testing.T) {
	registry := tools.NewRegistryWithEmergencyStop(approvalTestEmergencyStop{})
	local := &tools.LocalCommand{}
	registry.Register(local)

	b := &Bot{
		toolRegistry:    registry,
		approvalManager: &ApprovalManager{},
	}
	b.setCurrentChatID(0)
	b.InitializeApprovalSystem()

	if local.ApprovalFunc == nil {
		t.Fatal("InitializeApprovalSystem did not wire LocalCommand approval")
	}

	approved, err := local.ApprovalFunc("echo safe")
	if err != nil || !approved {
		t.Fatalf("safe command approval = %v, err = %v", approved, err)
	}

	approved, err = local.ApprovalFunc("rm -rf /tmp/approval-test")
	if err == nil || !strings.Contains(err.Error(), "no chat context") {
		t.Fatalf("dangerous command error = %v, want no-chat-context rejection", err)
	}
	if approved {
		t.Fatal("dangerous command was approved without chat context")
	}
}
