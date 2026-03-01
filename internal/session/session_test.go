package session

import "testing"

// --- keys.go ---

func TestAgentMain(t *testing.T) {
	if got := AgentMain("myagent"); got != "agent:myagent:main" {
		t.Errorf("AgentMain = %q", got)
	}
}

func TestTelegramDM(t *testing.T) {
	if got := TelegramDM("a1", 42); got != "agent:a1:telegram:dm:42" {
		t.Errorf("TelegramDM = %q", got)
	}
}

func TestTelegramGroup(t *testing.T) {
	if got := TelegramGroup("a1", -100); got != "agent:a1:telegram:group:-100" {
		t.Errorf("TelegramGroup = %q", got)
	}
}

func TestTelegramGroupThread(t *testing.T) {
	if got := TelegramGroupThread("a1", -100, 7); got != "agent:a1:telegram:group:-100:thread:7" {
		t.Errorf("TelegramGroupThread = %q", got)
	}
}

func TestSubagent(t *testing.T) {
	if got := Subagent("a1", "run-abc"); got != "agent:a1:subagent:run-abc" {
		t.Errorf("Subagent = %q", got)
	}
}

// --- router.go / routes.go ---

func TestResolve_AgentMainInternal(t *testing.T) {
	env := &Envelope{AgentID: "bot", Transport: TransportInternal}
	key, err := Resolve(env)
	if err != nil {
		t.Fatal(err)
	}
	if key != "agent:bot:main" {
		t.Errorf("got %q", key)
	}
}

func TestResolve_SubagentInternal(t *testing.T) {
	env := &Envelope{AgentID: "bot", Transport: TransportInternal, RunSlug: "run-1"}
	key, err := Resolve(env)
	if err != nil {
		t.Fatal(err)
	}
	if key != "agent:bot:subagent:run-1" {
		t.Errorf("got %q", key)
	}
}

func TestResolve_TelegramDMPerUser(t *testing.T) {
	env := &Envelope{
		AgentID:   "bot",
		Transport: TransportTelegram,
		IsDM:      true,
		DMScope:   "per_user",
		UserID:    99,
		ChatID:    99,
	}
	key, err := Resolve(env)
	if err != nil {
		t.Fatal(err)
	}
	if key != "agent:bot:telegram:dm:99" {
		t.Errorf("got %q", key)
	}
}

func TestResolve_TelegramDMShared(t *testing.T) {
	env := &Envelope{
		AgentID:   "bot",
		Transport: TransportTelegram,
		IsDM:      true,
		ChatID:    55,
	}
	key, err := Resolve(env)
	if err != nil {
		t.Fatal(err)
	}
	if key != "agent:bot:telegram:group:55" {
		t.Errorf("got %q", key)
	}
}

func TestResolve_TelegramGroup(t *testing.T) {
	env := &Envelope{
		AgentID:   "bot",
		Transport: TransportTelegram,
		ChatID:    -100,
	}
	key, err := Resolve(env)
	if err != nil {
		t.Fatal(err)
	}
	if key != "agent:bot:telegram:group:-100" {
		t.Errorf("got %q", key)
	}
}

func TestResolve_TelegramThread(t *testing.T) {
	env := &Envelope{
		AgentID:   "bot",
		Transport: TransportTelegram,
		ChatID:    -100,
		ThreadID:  5,
	}
	key, err := Resolve(env)
	if err != nil {
		t.Fatal(err)
	}
	if key != "agent:bot:telegram:group:-100:thread:5" {
		t.Errorf("got %q", key)
	}
}

func TestResolve_MissingAgentID(t *testing.T) {
	env := &Envelope{Transport: TransportInternal}
	_, err := Resolve(env)
	if err == nil {
		t.Fatal("expected error for missing AgentID")
	}
}

func TestResolve_UnknownTransport(t *testing.T) {
	env := &Envelope{AgentID: "bot", Transport: "grpc"}
	_, err := Resolve(env)
	if err == nil {
		t.Fatal("expected error for unknown transport")
	}
}

func TestRouter_ResolveRecordsRoute(t *testing.T) {
	rs := NewRouteStore()
	router := NewRouter(rs)

	env := &Envelope{
		AgentID:   "bot",
		Transport: TransportTelegram,
		ChatID:    -100,
		UserID:    7,
	}

	key, agentID, err := router.Resolve(env)
	if err != nil {
		t.Fatal(err)
	}
	if agentID != "bot" {
		t.Errorf("agentID = %q", agentID)
	}

	route, ok := rs.Get(key)
	if !ok {
		t.Fatal("route not stored")
	}
	if route.ChatID != -100 || route.UserID != 7 || route.Channel != TransportTelegram {
		t.Errorf("unexpected route: %+v", route)
	}
}

func TestRouteStore_SetGetDelete(t *testing.T) {
	rs := NewRouteStore()

	rs.Set("key1", DeliveryRoute{Channel: TransportTelegram, ChatID: 1})
	r, ok := rs.Get("key1")
	if !ok || r.ChatID != 1 {
		t.Fatalf("Get after Set failed: ok=%v route=%+v", ok, r)
	}

	rs.Delete("key1")
	_, ok = rs.Get("key1")
	if ok {
		t.Fatal("key still present after Delete")
	}
}
