package tools

import "context"

type userMessageKey struct{}

// WithUserMessage attaches the text of the current user turn to ctx so tools
// can check the user's own wording (deep_think verifies that a strong model
// was actually asked for). It is evidence, never an instruction channel.
func WithUserMessage(ctx context.Context, text string) context.Context {
	return context.WithValue(ctx, userMessageKey{}, text)
}

// UserMessageFromContext returns the current user turn text, or "".
func UserMessageFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(userMessageKey{}).(string); ok {
		return v
	}
	return ""
}
