package gmailtriage

import (
	"context"
	"fmt"
	"strings"
)

// confirmation proves that a Telegram button confirmed one specific pending
// action. Its only constructor is Store.consumeAction via Service.confirm, so
// no code path can trash or send without a used, unexpired action row.
type confirmation struct {
	action Action
}

// policyMailbox applies the profile policy to every mailbox call.
type policyMailbox struct {
	profile Profile
	reader  Reader
	writer  Writer // always nil for read-only profiles
}

func newPolicyMailbox(p Profile, r Reader, w Writer) *policyMailbox {
	if p.ReadOnly() {
		w = nil
	}
	return &policyMailbox{profile: p, reader: r, writer: w}
}

func (m *policyMailbox) search(ctx context.Context, query string, max int) ([]string, error) {
	return m.reader.Search(ctx, query, max)
}

func (m *policyMailbox) thread(ctx context.Context, id string) (Thread, error) {
	return m.reader.Thread(ctx, id)
}

// canOrganize reports whether the profile may label, archive and mark read.
func (m *policyMailbox) canOrganize() bool {
	return !m.profile.ReadOnly() && m.writer != nil
}

func (m *policyMailbox) ensureLabels(ctx context.Context, names []string) error {
	if !m.canOrganize() {
		return ErrForbidden
	}
	return m.writer.EnsureLabels(ctx, names)
}

// organize labels, archives or marks read. Trash and send never pass here.
func (m *policyMailbox) organize(ctx context.Context, threadID string, add, remove []string) error {
	if !m.canOrganize() {
		return ErrForbidden
	}
	for _, l := range append(append([]string{}, add...), remove...) {
		switch strings.ToUpper(l) {
		case "TRASH", "SPAM", "SENT", "DRAFT":
			return fmt.Errorf("%w: label %s needs a confirmed action", ErrForbidden, l)
		}
	}
	return m.writer.ModifyThread(ctx, threadID, add, remove)
}

func (m *policyMailbox) checkConfirmed(c *confirmation, kind string, item Item) error {
	if m.profile.ReadOnly() || m.writer == nil {
		return ErrForbidden
	}
	if c == nil || c.action.Kind != kind || c.action.ItemID != item.ID || c.action.Profile != m.profile.Name || item.Profile != m.profile.Name {
		return fmt.Errorf("%w: %s needs a confirmed Telegram action", ErrForbidden, kind)
	}
	if c.action.ChatID != m.profile.ChatID {
		return fmt.Errorf("%w: confirmation came from another chat", ErrForbidden)
	}
	return nil
}

func (m *policyMailbox) trash(ctx context.Context, c *confirmation, item Item) error {
	if err := m.checkConfirmed(c, ActionTrash, item); err != nil {
		return err
	}
	return m.writer.TrashThread(ctx, item.ThreadID)
}

func (m *policyMailbox) send(ctx context.Context, c *confirmation, item Item, r Reply) error {
	if err := m.checkConfirmed(c, ActionSend, item); err != nil {
		return err
	}
	if r.ThreadID != item.ThreadID {
		return fmt.Errorf("%w: reply thread does not match the confirmed item", ErrForbidden)
	}
	return m.writer.SendReply(ctx, r)
}
