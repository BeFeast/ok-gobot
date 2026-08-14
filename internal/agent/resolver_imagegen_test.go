package agent

import (
	"testing"

	"ok-gobot/internal/tools"
)

// resolverMediaSender implements tools.MediaSender for resolver tests.
type resolverMediaSender struct{ calls int }

func (s *resolverMediaSender) SendPhotoToChat(_ int64, _, _ string) error {
	s.calls++
	return nil
}

func TestBuildToolRegistry_ImageGenReboundWithChatDelivery(t *testing.T) {
	t.Parallel()

	baseTool := tools.NewImageTool("sk-test", "")
	base := tools.NewRegistry()
	base.Register(baseTool)

	resolver := &RunResolver{
		ToolRegistry: base,
		MediaSender:  &resolverMediaSender{},
	}

	reg := resolver.buildToolRegistry(42, &AgentProfile{}, false, nil)

	got, ok := reg.Get("image_gen")
	if !ok {
		t.Fatal("image_gen missing after per-chat injection")
	}
	if got == tools.Tool(baseTool) {
		t.Fatal("image_gen was not rebound with chat delivery")
	}
	if _, ok := got.(*tools.ImageTool); !ok {
		t.Fatalf("rebound image_gen has unexpected type %T", got)
	}
}

func TestBuildToolRegistry_ImageGenKeptUnboundWithoutSenderOrChat(t *testing.T) {
	t.Parallel()

	baseTool := tools.NewImageTool("sk-test", "")

	cases := []struct {
		name     string
		resolver *RunResolver
		chatID   int64
	}{
		{name: "no media sender", resolver: &RunResolver{}, chatID: 42},
		{name: "no chat", resolver: &RunResolver{MediaSender: &resolverMediaSender{}}, chatID: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := tools.NewRegistry()
			base.Register(baseTool)
			tc.resolver.ToolRegistry = base

			reg := tc.resolver.buildToolRegistry(tc.chatID, &AgentProfile{}, false, nil)

			got, ok := reg.Get("image_gen")
			if !ok {
				t.Fatal("image_gen missing")
			}
			if got != tools.Tool(baseTool) {
				t.Fatal("image_gen was rebound without sender+chat")
			}
		})
	}
}

func TestBuildToolRegistry_ImageGenRespectsProfileAllowlist(t *testing.T) {
	t.Parallel()

	base := tools.NewRegistry()
	base.Register(tools.NewImageTool("sk-test", ""))

	resolver := &RunResolver{
		ToolRegistry: base,
		MediaSender:  &resolverMediaSender{},
	}
	profile := &AgentProfile{AllowedTools: []string{"file"}}

	reg := resolver.buildToolRegistry(42, profile, false, nil)

	if _, ok := reg.Get("image_gen"); ok {
		t.Fatal("image_gen must not be re-injected past the profile allowlist")
	}
}
