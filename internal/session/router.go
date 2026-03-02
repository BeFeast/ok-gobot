package session

import "fmt"

// Envelope represents an inbound message from any transport layer.
// The caller populates the fields that are relevant for the source transport.
type Envelope struct {
	// AgentID is the identifier of the agent that should handle this message.
	AgentID string

	// Transport identifies the source channel.
	Transport Transport

	// Telegram-specific fields:

	// ChatID is the Telegram chat identifier (group or private).
	ChatID int64
	// UserID is the Telegram user identifier.
	UserID int64
	// ThreadID is the forum topic ID; 0 for non-threaded messages.
	ThreadID int
	// IsDM is true when the message arrives in a private (DM) chat.
	IsDM bool
	// DMScope controls how DM session keys are constructed.
	// "per_user" → one session per user.
	// ""         → shared session keyed by ChatID (default group-like behaviour).
	DMScope string

	// Internal / sub-agent fields:

	// RunSlug identifies a sub-agent execution when Transport=TransportInternal.
	RunSlug string
}

// Resolve returns the canonical session key for env.
// It returns an error when AgentID is empty or Transport is unrecognised.
func Resolve(env *Envelope) (string, error) {
	if env.AgentID == "" {
		return "", fmt.Errorf("session: envelope missing AgentID")
	}

	switch env.Transport {
	case TransportTelegram:
		return resolveTelegram(env)
	case TransportInternal:
		if env.RunSlug != "" {
			return Subagent(env.AgentID, env.RunSlug), nil
		}
		return AgentMain(env.AgentID), nil
	default:
		return "", fmt.Errorf("session: unknown transport %q", env.Transport)
	}
}

func resolveTelegram(env *Envelope) (string, error) {
	if env.IsDM {
		if env.DMScope == "per_user" {
			return TelegramDM(env.AgentID, env.UserID), nil
		}
		// Shared DM: fall through to group key using ChatID.
		return TelegramGroup(env.AgentID, env.ChatID), nil
	}

	if env.ThreadID != 0 {
		return TelegramGroupThread(env.AgentID, env.ChatID, env.ThreadID), nil
	}

	return TelegramGroup(env.AgentID, env.ChatID), nil
}

// Router resolves inbound envelopes to canonical session keys and records
// the reply route so that outbound messages can find the correct chat.
type Router struct {
	routes *RouteStore
}

// NewRouter creates a Router backed by the given RouteStore.
func NewRouter(rs *RouteStore) *Router {
	return &Router{routes: rs}
}

// Resolve resolves env to a canonical session key, records the delivery route,
// and returns the key and the AgentID extracted from the envelope.
func (r *Router) Resolve(env *Envelope) (key, agentID string, err error) {
	key, err = Resolve(env)
	if err != nil {
		return "", "", err
	}

	r.routes.Set(key, DeliveryRoute{
		Channel:  env.Transport,
		ChatID:   env.ChatID,
		ThreadID: env.ThreadID,
		UserID:   env.UserID,
	})

	return key, env.AgentID, nil
}
