package gmailtriage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// NativeScheduler is the cron surface the service needs (internal/cron.Scheduler).
type NativeScheduler interface {
	ReplaceNativeJobs(group string, specs []string, fn func()) error
	NativeNextRun(group string) time.Time
}

// MailboxFactory builds the Gmail access of a profile. Read-only profiles get
// no writer whatever the factory returns.
type MailboxFactory func(Profile) (Reader, Writer)

// GogMailboxFactory returns gog-backed mailboxes. A read-only profile gets a
// client that refuses mutating gog commands before exec.
func GogMailboxFactory(binary string, env []string) MailboxFactory {
	return func(p Profile) (Reader, Writer) {
		client := &GogClient{Binary: binary, Account: p.Account, Env: env, ReadOnly: p.ReadOnly()}
		if p.ReadOnly() {
			return client, nil
		}
		return client, client
	}
}

// Options wires the service dependencies.
type Options struct {
	Store     *Store
	Completer Completer
	Mailboxes MailboxFactory
	// SkillText returns the current SKILL.md body ("" when not installed).
	SkillText func() string
	Now       func() time.Time
}

// Service runs triage for the configured profiles.
type Service struct {
	profiles  []Profile
	store     *Store
	llm       Completer
	mailboxes map[string]*policyMailbox
	skillText func() string
	now       func() time.Time

	locksMu sync.Mutex
	locks   map[string]*sync.Mutex

	confirmMu sync.Mutex

	schedMu   sync.Mutex
	scheduler NativeScheduler
	fire      func(Profile)
}

// ErrBusy means a run of the same profile is in progress.
var ErrBusy = errors.New("gmail_triage: a run is already in progress")

// ErrNoProfile means the chat has no triage profile.
var ErrNoProfile = errors.New("gmail_triage: this chat has no mailbox profile")

// New validates the config and builds the service.
func New(cfg Config, opts Options) (*Service, error) {
	profiles, err := cfg.Resolve()
	if err != nil {
		return nil, err
	}
	if opts.Store == nil || opts.Completer == nil || opts.Mailboxes == nil {
		return nil, errors.New("gmail_triage: store, completer and mailboxes are required")
	}
	s := &Service{
		profiles:  profiles,
		store:     opts.Store,
		llm:       opts.Completer,
		mailboxes: map[string]*policyMailbox{},
		skillText: opts.SkillText,
		now:       opts.Now,
		locks:     map[string]*sync.Mutex{},
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.skillText == nil {
		s.skillText = func() string { return "" }
	}
	for _, p := range profiles {
		r, w := opts.Mailboxes(p)
		if r == nil {
			return nil, fmt.Errorf("gmail_triage: no mailbox reader for profile %q", p.Name)
		}
		s.mailboxes[p.Name] = newPolicyMailbox(p, r, w)
	}
	return s, nil
}

// Profiles returns the configured profiles.
func (s *Service) Profiles() []Profile { return append([]Profile(nil), s.profiles...) }

// ProfileForChat finds the profile delivered to a chat.
func (s *Service) ProfileForChat(chatID int64) (Profile, error) {
	for _, p := range s.profiles {
		if p.ChatID == chatID {
			return p, nil
		}
	}
	return Profile{}, ErrNoProfile
}

// Store exposes persisted state to the delivery layer.
func (s *Service) Store() *Store { return s.store }

func (s *Service) lock(name string) *sync.Mutex {
	s.locksMu.Lock()
	defer s.locksMu.Unlock()
	if s.locks[name] == nil {
		s.locks[name] = &sync.Mutex{}
	}
	return s.locks[name]
}

// Digest is the result of a run, ready for delivery.
type Digest struct {
	Profile  Profile
	Items    []Item // attention items, bucket order
	Quiet    []Item // count-only bucket (Bulk / Ignore)
	Overflow int    // attention items held back for the next digest
	Scanned  int
	FirstRun bool
	Errors   []string
}

// Empty reports a digest with nothing that needs the user.
func (d Digest) Empty() bool { return len(d.Items) == 0 }

// Archived counts quiet items the assistant archived.
func (d Digest) Archived() int {
	n := 0
	for _, it := range d.Quiet {
		if it.Archived {
			n++
		}
	}
	return n
}

const (
	runOverlap   = 15 * time.Minute
	waitingScope = "newer_than:14d"
	fetchWorkers = 4
)

// maxThreadRetries bounds how often a thread that fails to fetch, classify
// or label is retried before it is reported and dropped.
const maxThreadRetries = 3

// Run fetches new mail, classifies it, applies the profile policy and
// returns the undelivered items as a digest. Mark items reported after delivery.
func (s *Service) Run(ctx context.Context, p Profile) (Digest, error) {
	return s.RunAndDeliver(ctx, p, nil)
}

// RunAndDeliver runs triage and hands the digest to deliver while still
// holding the profile lock, so two runs never deliver the same items.
func (s *Service) RunAndDeliver(ctx context.Context, p Profile, deliver func(Digest) error) (Digest, error) {
	mu := s.lock(p.Name)
	if !mu.TryLock() {
		return Digest{}, ErrBusy
	}
	defer mu.Unlock()
	digest, err := s.run(ctx, p)
	if err != nil || deliver == nil {
		return digest, err
	}
	return digest, deliver(digest)
}

func (s *Service) run(ctx context.Context, p Profile) (Digest, error) {
	mb := s.mailboxes[p.Name]
	if mb == nil {
		return Digest{}, ErrNoProfile
	}
	now := s.now()
	digest := Digest{Profile: p}

	lastRun, err := s.store.LastRun(p.Name)
	if err != nil {
		return digest, err
	}
	var query string
	if lastRun.IsZero() {
		digest.FirstRun = true
		query = fmt.Sprintf("(%s) newer_than:%dd", p.Query, p.InitialLookbackDays)
	} else {
		query = fmt.Sprintf("(%s) after:%d", p.Query, lastRun.Add(-runOverlap).Unix())
	}
	ids, err := mb.search(ctx, query, p.MaxThreads)
	if err != nil {
		return digest, fmt.Errorf("search: %w", err)
	}
	if len(ids) >= p.MaxThreads {
		digest.Errors = append(digest.Errors, fmt.Sprintf(tr(p.Language,
			"only the newest %d threads of this window were checked (max_threads)",
			"проверены только %d самых новых "+ruPlural(len(ids), "тред", "треда", "тредов")+" окна (max_threads)"), len(ids)))
	}
	// Threads found only through sent mail are Waiting candidates, nothing
	// else: an old conversation the other side answered long ago is not news.
	waitingOnly := map[string]bool{}
	if p.Taxonomy == TaxonomyOwner {
		sent, err := mb.search(ctx, "in:sent "+waitingScope, p.MaxThreads)
		if err != nil {
			digest.Errors = append(digest.Errors, "sent search: "+err.Error())
		}
		inbox := make(map[string]bool, len(ids))
		for _, id := range ids {
			inbox[id] = true
		}
		for _, id := range sent {
			if !inbox[id] {
				waitingOnly[id] = true
			}
		}
		ids = appendUnique(ids, sent)
	}
	// Threads that failed before are retried even outside the new window.
	retry, err := s.store.RetryThreads(p.Name)
	if err != nil {
		return digest, err
	}
	for id := range retry {
		ids = appendUnique(ids, []string{id})
	}
	failed := map[string]bool{}

	threads, fetchFailed, fetchErrs := s.fetchChanged(ctx, p, mb, ids)
	digest.Errors = append(digest.Errors, fetchErrs...)
	for _, id := range fetchFailed {
		failed[id] = true
	}
	digest.Scanned = len(threads)

	rules, err := s.store.Rules(p.Name)
	if err != nil {
		return digest, err
	}
	decided := map[string]Item{}
	var toLLM []Thread
	for _, t := range threads {
		if waitingOnly[t.ID] && !p.isMine(t.Last()) {
			continue
		}
		it, decide := s.decide(p, rules, t, now)
		switch decide {
		case decisionSkip:
		case decisionDone:
			decided[t.ID] = it
		case decisionLLM:
			toLLM = append(toLLM, t)
			decided[t.ID] = it
		}
	}
	if len(toLLM) > 0 {
		examples, err := s.store.Examples(p.Name, 25)
		if err != nil {
			return digest, err
		}
		verdicts, err := classify(ctx, s.llm, p, s.skillText(), rules, examples, toLLM)
		if err != nil {
			// Keep what was classified; the rest is retried next run.
			digest.Errors = append(digest.Errors, err.Error())
		}
		for _, t := range toLLM {
			v, ok := verdicts[t.ID]
			if !ok {
				delete(decided, t.ID)
				failed[t.ID] = true
				continue
			}
			it := decided[t.ID]
			it.Bucket, it.Why, it.Uncertain, it.Draft, it.Source = v.Bucket, v.Why, v.Uncertain, v.Draft, "llm"
			decided[t.ID] = it
		}
	}

	if mb.canOrganize() && len(decided) > 0 {
		errs, labelFailed := s.organizeAll(ctx, p, mb, decided)
		digest.Errors = append(digest.Errors, errs...)
		for _, id := range labelFailed {
			delete(decided, id)
			failed[id] = true
		}
	}
	for _, t := range threads {
		it, ok := decided[t.ID]
		if !ok {
			continue
		}
		stored, err := s.store.UpsertItem(it)
		if err != nil {
			return digest, err
		}
		// The thread changed: buttons on its older card no longer match.
		if err := s.store.CancelActions(p.Name, stored.ID); err != nil {
			return digest, err
		}
	}

	next := map[string]int{}
	gaveUp := 0
	for id := range failed {
		if n := retry[id] + 1; n < maxThreadRetries {
			next[id] = n
		} else {
			gaveUp++
		}
	}
	if gaveUp > 0 {
		digest.Errors = append(digest.Errors, fmt.Sprintf(tr(p.Language,
			"%d threads failed %d times and were skipped", "пропущено тредов: %d, не удалось обработать за %d попытки"), gaveUp, maxThreadRetries))
	}
	if err := s.store.SetRetryThreads(p.Name, next); err != nil {
		return digest, err
	}
	// The window always advances; failures ride the retry list instead of
	// pinning the window open.
	if err := s.store.SetLastRun(p.Name, now); err != nil {
		return digest, err
	}
	return s.pendingDigest(p, digest)
}

// Pending returns undelivered items as a digest without fetching mail.
func (s *Service) Pending(p Profile) (Digest, error) {
	return s.pendingDigest(p, Digest{Profile: p})
}

func (s *Service) pendingDigest(p Profile, digest Digest) (Digest, error) {
	items, err := s.store.NewItems(p.Name)
	if err != nil {
		return digest, err
	}
	order := map[string]int{}
	for i, b := range p.Taxonomy.Buckets() {
		order[b.Key] = i
	}
	var attention []Item
	for _, it := range items {
		def, _ := p.Taxonomy.Bucket(it.Bucket)
		if def.CountOnly {
			digest.Quiet = append(digest.Quiet, it)
			continue
		}
		attention = append(attention, it)
	}
	sort.SliceStable(attention, func(i, j int) bool {
		return order[attention[i].Bucket] < order[attention[j].Bucket]
	})
	if len(attention) > p.MaxItems {
		digest.Overflow = len(attention) - p.MaxItems
		attention = attention[:p.MaxItems]
	}
	digest.Items = attention
	return digest, nil
}

func appendUnique(a, b []string) []string {
	seen := make(map[string]bool, len(a))
	for _, id := range a {
		seen[id] = true
	}
	for _, id := range b {
		if !seen[id] {
			seen[id] = true
			a = append(a, id)
		}
	}
	return a
}

// fetchChanged loads threads whose newest message was not triaged yet.
func (s *Service) fetchChanged(ctx context.Context, p Profile, mb *policyMailbox, ids []string) ([]Thread, []string, []string) {
	type result struct {
		idx int
		t   Thread
		err error
	}
	jobs := make(chan int)
	results := make(chan result)
	var wg sync.WaitGroup
	for w := 0; w < fetchWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				t, err := mb.thread(ctx, ids[i])
				results <- result{idx: i, t: t, err: err}
			}
		}()
	}
	go func() {
		for i := range ids {
			if ctx.Err() != nil {
				break
			}
			jobs <- i
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	fetched := make([]*Thread, len(ids))
	var errs, failed []string
	for r := range results {
		if r.err != nil {
			failed = append(failed, ids[r.idx])
			if len(errs) < 3 {
				errs = append(errs, "thread: "+r.err.Error())
			}
			continue
		}
		t := r.t
		fetched[r.idx] = &t
	}
	var out []Thread
	for _, t := range fetched {
		if t == nil || len(t.Messages) == 0 {
			continue
		}
		last := t.Last()
		if last.HasLabel("TRASH") || last.HasLabel("SPAM") {
			continue
		}
		seen, err := s.store.LastMessageID(p.Name, t.ID)
		if err != nil {
			errs = append(errs, err.Error())
			failed = append(failed, t.ID)
			continue
		}
		if seen == last.ID {
			continue
		}
		out = append(out, *t)
	}
	return out, failed, errs
}

type decision int

const (
	decisionSkip decision = iota
	decisionDone
	decisionLLM
)

func (p Profile) isMine(m Message) bool {
	return m.HasLabel("SENT") || m.FromAddress() == p.Account
}

// lastInbound is the newest message not written by the owner.
func (p Profile) lastInbound(t Thread) Message {
	for i := len(t.Messages) - 1; i >= 0; i-- {
		if !p.isMine(t.Messages[i]) {
			return t.Messages[i]
		}
	}
	return t.Last()
}

// decide applies the deterministic steps: waiting, user rules, pre-filter.
func (s *Service) decide(p Profile, rules []Rule, t Thread, now time.Time) (Item, decision) {
	last := t.Last()
	inbound := p.lastInbound(t)
	it := Item{
		Profile:       p.Name,
		ThreadID:      t.ID,
		LastMessageID: last.ID,
		Sender:        inbound.FromAddress(),
		SenderName:    inbound.FromName(),
		ReplyTo:       addressOf(inbound.ReplyTo),
		Subject:       t.Subject(),
		Snippet:       truncate(oneLine(firstNonEmpty(inbound.Snippet, inbound.Body), 400), 300),
		MessageTime:   last.Time,
	}
	if p.isMine(last) {
		if p.Taxonomy != TaxonomyOwner {
			return it, decisionSkip
		}
		days := Workdays(last.Time, now, p.location())
		if days < p.WaitingWorkdays {
			return it, decisionSkip // ball is in their court, too early to chase
		}
		// Waiting is about the person we wrote to. Notes to self and mail to
		// no-reply addresses wait for nobody.
		it.Sender = addressOf(last.To)
		if it.Sender == "" {
			it.Sender = inbound.FromAddress()
		}
		if it.Sender == "" || it.Sender == p.Account || isNoReply(it.Sender) {
			return it, decisionSkip
		}
		it.SenderName = it.Sender
		it.ReplyTo = ""
		it.Bucket = BucketWaiting
		it.Why = fmt.Sprintf(tr(p.Language, "no reply for %d workdays", "нет ответа %d раб. дн."), days)
		it.Source = "waiting"
		return it, decisionDone
	}
	if r, ok := matchRule(rules, it.Sender); ok {
		it.Bucket = r.Bucket
		it.Why = fmt.Sprintf(tr(p.Language, "your rule: %s %s", "твоё правило: %s %s"), r.Scope, r.Value)
		it.Source = "rule"
		return it, decisionDone
	}
	if bucket, why, ok := prefilter(p, t); ok {
		it.Bucket, it.Why, it.Source = bucket, why, "prefilter"
		return it, decisionDone
	}
	return it, decisionLLM
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// triageLabels lists every Gmail label the profile manages.
func (p Profile) triageLabels() []string {
	var out []string
	for _, b := range p.Taxonomy.Buckets() {
		out = append(out, p.GmailLabel(b.Key))
	}
	return out
}

// labelChange computes the Gmail change for putting a thread into a bucket.
// Quiet mail is archived and marked read; any other bucket keeps (or brings
// back) the thread in the inbox when it was archived by triage.
func (p Profile) labelChange(bucket string, wasArchived bool) (add, remove []string, archived bool) {
	target := p.GmailLabel(bucket)
	add = []string{target}
	for _, l := range p.triageLabels() {
		if l != target {
			remove = append(remove, l)
		}
	}
	if bucket == p.Taxonomy.QuietBucket() {
		remove = append(remove, "INBOX", "UNREAD")
		return add, remove, true
	}
	if wasArchived {
		add = append(add, "INBOX")
	}
	return add, remove, false
}

// organizeAll labels the decided threads; it returns errors and the threads
// that could not be labelled (they are retried, not recorded).
func (s *Service) organizeAll(ctx context.Context, p Profile, mb *policyMailbox, decided map[string]Item) ([]string, []string) {
	if err := mb.ensureLabels(ctx, p.triageLabels()); err != nil {
		ids := make([]string, 0, len(decided))
		for id := range decided {
			ids = append(ids, id)
		}
		return []string{"labels: " + err.Error()}, ids
	}
	var errs, failed []string
	for id, it := range decided {
		add, remove, archived := p.labelChange(it.Bucket, false)
		if err := mb.organize(ctx, id, add, remove); err != nil {
			failed = append(failed, id)
			if len(errs) < 3 {
				errs = append(errs, "label: "+err.Error())
			}
			continue
		}
		it.Archived = archived
		decided[id] = it
	}
	return errs, failed
}

// ---- confirmed actions ----

// NewAction mints a pending trash or send button for an item.
func (s *Service) NewAction(p Profile, item Item, kind string) (Action, error) {
	if p.ReadOnly() {
		return Action{}, ErrForbidden
	}
	switch kind {
	case ActionTrash:
	case ActionSend:
		if strings.TrimSpace(item.Draft) == "" {
			return Action{}, errors.New("no draft to send")
		}
	default:
		return Action{}, fmt.Errorf("unknown action %q", kind)
	}
	return s.store.CreateAction(p.Name, item.ID, kind, p.ChatID, actionFingerprint(kind, item))
}

// Recipient is where a reply to the item goes: Reply-To when set, else From.
func (it Item) Recipient() string {
	if it.ReplyTo != "" {
		return it.ReplyTo
	}
	return it.Sender
}

// actionFingerprint captures what the user saw on the card when the button
// was made: the newest message and, for send, recipient and draft.
func actionFingerprint(kind string, it Item) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", kind, it.LastMessageID)
	if kind == ActionSend {
		fmt.Fprintf(h, "%s\n%s", it.Recipient(), it.Draft)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Confirm executes a pending action after its Telegram button was pressed in
// chatID. The action is consumed first, so a double tap cannot repeat it.
func (s *Service) Confirm(ctx context.Context, actionID string, chatID int64) (Item, Action, error) {
	p, err := s.ProfileForChat(chatID)
	if err != nil {
		return Item{}, Action{}, err
	}
	if p.ReadOnly() {
		return Item{}, Action{}, ErrForbidden
	}
	// One confirmation at a time: Trash and Send pressed together must not
	// both run.
	s.confirmMu.Lock()
	defer s.confirmMu.Unlock()
	a, err := s.store.consumeAction(actionID, chatID)
	if err != nil {
		return Item{}, Action{}, err
	}
	item, err := s.store.Item(p.Name, a.ItemID)
	if err != nil {
		return Item{}, a, err
	}
	switch item.Status {
	case StatusSent, StatusTrashed, StatusSkipped:
		return item, a, ErrActionUnavailable
	}
	if a.Fingerprint != actionFingerprint(a.Kind, item) {
		// The thread, recipient or draft changed since the card was shown.
		return item, a, ErrActionUnavailable
	}
	c := &confirmation{action: a}
	mb := s.mailboxes[p.Name]
	switch a.Kind {
	case ActionTrash:
		if err := mb.trash(ctx, c, item); err != nil {
			return item, a, err
		}
		item.Status = StatusTrashed
	case ActionSend:
		reply := Reply{ThreadID: item.ThreadID, ReplyToMessageID: item.LastMessageID, To: item.Recipient(), Subject: replySubject(item.Subject), Body: item.Draft}
		if err := mb.send(ctx, c, item, reply); err != nil {
			return item, a, err
		}
		item.Status = StatusSent
	default:
		return item, a, fmt.Errorf("unknown action %q", a.Kind)
	}
	if err := s.store.SetStatus(item.ID, item.Status); err != nil {
		log.Printf("[gmail_triage] status update failed for item %d: %v", item.ID, err)
	}
	_ = s.store.CancelActions(p.Name, item.ID)
	return item, a, nil
}

func replySubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "Re:"
	}
	if strings.HasPrefix(strings.ToLower(subject), "re:") {
		return subject
	}
	return "Re: " + subject
}

// ---- learning ----

// Scope values for corrections.
const (
	ScopeSender = "sender"
	ScopeDomain = "domain"
	ScopeOnce   = "once"
)

// Recategorize moves an item to another bucket. Scope sender/domain also
// stores a standing rule; every correction becomes a few-shot example.
// Assistant profiles relabel the thread in Gmail.
func (s *Service) Recategorize(ctx context.Context, p Profile, itemID int64, bucket, scope string) (Item, error) {
	if _, ok := p.Taxonomy.Bucket(bucket); !ok || bucket == BucketWaiting {
		return Item{}, fmt.Errorf("bucket %q cannot be chosen", bucket)
	}
	item, err := s.store.Item(p.Name, itemID)
	if err != nil {
		return Item{}, err
	}
	why := tr(p.Language, "your correction", "твоя поправка")
	switch scope {
	case ScopeSender, ScopeDomain:
		value := item.Sender
		if scope == ScopeDomain {
			value = domainOf(item.Sender)
		}
		r, err := s.AddRule(p, scope, value, bucket, "")
		if err != nil {
			return Item{}, err
		}
		why = fmt.Sprintf(tr(p.Language, "your rule: %s %s", "твоё правило: %s %s"), r.Scope, r.Value)
	case ScopeOnce:
	default:
		return Item{}, fmt.Errorf("unknown scope %q", scope)
	}
	if err := s.store.AddExample(p.Name, Example{Sender: item.Sender, Subject: item.Subject, Snippet: item.Snippet, Bucket: bucket}); err != nil {
		return Item{}, err
	}
	archived := item.Archived
	if mb := s.mailboxes[p.Name]; mb.canOrganize() {
		add, remove, arch := p.labelChange(bucket, item.Archived)
		if err := mb.ensureLabels(ctx, p.triageLabels()); err != nil {
			return Item{}, err
		}
		if err := mb.organize(ctx, item.ThreadID, add, remove); err != nil {
			return Item{}, err
		}
		archived = arch
	}
	if err := s.store.SetBucket(item.ID, bucket, why, archived); err != nil {
		return Item{}, err
	}
	if bucket != BucketReply {
		_ = s.store.CancelActions(p.Name, item.ID)
	}
	return s.store.Item(p.Name, item.ID)
}

// ConfirmBucket records that the classification was right.
func (s *Service) ConfirmBucket(p Profile, itemID int64) (Item, error) {
	item, err := s.store.Item(p.Name, itemID)
	if err != nil {
		return Item{}, err
	}
	return item, s.store.AddExample(p.Name, Example{Sender: item.Sender, Subject: item.Subject, Snippet: item.Snippet, Bucket: item.Bucket, Note: "confirmed"})
}

// AddNote stores a free-text remark about an item as a few-shot example.
func (s *Service) AddNote(p Profile, itemID int64, note string) (Item, error) {
	item, err := s.store.Item(p.Name, itemID)
	if err != nil {
		return Item{}, err
	}
	note = strings.TrimSpace(note)
	if note == "" {
		return item, errors.New("empty note")
	}
	return item, s.store.AddExample(p.Name, Example{Sender: item.Sender, Subject: item.Subject, Snippet: item.Snippet, Bucket: item.Bucket, Note: note})
}

// MaxDraftChars keeps a card with its draft under Telegram's 4096 characters.
const MaxDraftChars = 3000

// UpdateDraft replaces the draft of a reply item.
func (s *Service) UpdateDraft(p Profile, itemID int64, draft string) (Item, error) {
	draft = strings.TrimSpace(draft)
	if draft == "" {
		return Item{}, errors.New("empty draft")
	}
	item, err := s.store.Item(p.Name, itemID)
	if err != nil {
		return Item{}, err
	}
	if err := s.store.CancelActions(p.Name, item.ID); err != nil {
		return Item{}, err
	}
	if err := s.store.SetDraft(item.ID, truncate(draft, MaxDraftChars)); err != nil {
		return Item{}, err
	}
	return s.store.Item(p.Name, item.ID)
}

// Skip closes an item without action.
func (s *Service) Skip(p Profile, itemID int64) (Item, error) {
	item, err := s.store.Item(p.Name, itemID)
	if err != nil {
		return Item{}, err
	}
	_ = s.store.CancelActions(p.Name, item.ID)
	if err := s.store.SetStatus(item.ID, StatusSkipped); err != nil {
		return Item{}, err
	}
	item.Status = StatusSkipped
	return item, nil
}

// ResolveBucket accepts a bucket key or its display name in either language.
func ResolveBucket(t Taxonomy, raw string) (string, bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.ReplaceAll(raw, "-", "_")
	for _, b := range t.Buckets() {
		if raw == b.Key || raw == strings.ToLower(b.Label) {
			return b.Key, true
		}
		for _, names := range bucketNames {
			if strings.ToLower(names[b.Key]) == raw {
				return b.Key, true
			}
		}
	}
	switch raw {
	case "needs reply", "needsreply", "reply_needed":
		if t == TaxonomySales {
			return BucketNeeds, true
		}
		return BucketReply, true
	case "spam", "fan mail", "fanmail", "fan_mail":
		return t.QuietBucket(), true
	case "ignore":
		return t.QuietBucket(), true
	}
	return "", false
}

// AddRule stores a standing rule by sender address or domain.
func (s *Service) AddRule(p Profile, scope, value, bucket, note string) (Rule, error) {
	scope, err := normalizeScope(scope)
	if err != nil {
		return Rule{}, err
	}
	key, ok := ResolveBucket(p.Taxonomy, bucket)
	if !ok || key == BucketWaiting {
		return Rule{}, fmt.Errorf("unknown bucket %q (use one of: %s)", bucket, strings.Join(p.ruleBuckets(), ", "))
	}
	value = strings.ToLower(strings.TrimSpace(value))
	switch scope {
	case ScopeSender:
		addr := addressOf(value)
		if addr == "" {
			return Rule{}, fmt.Errorf("%q is not an email address", value)
		}
		value = addr
	case ScopeDomain:
		value = strings.TrimPrefix(value, "@")
		if a := addressOf(value); a != "" {
			value = domainOf(a)
		}
		if !strings.Contains(value, ".") || strings.ContainsAny(value, " @/") {
			return Rule{}, fmt.Errorf("%q is not a domain", value)
		}
		if IsFreemail(value) {
			return Rule{}, fmt.Errorf("%s is a public mail provider; use a sender rule instead", value)
		}
	}
	return s.store.PutRule(Rule{Profile: p.Name, Scope: scope, Value: value, Bucket: key, Note: truncate(strings.TrimSpace(note), 300)})
}

func (p Profile) ruleBuckets() []string {
	var out []string
	for _, b := range p.Taxonomy.Buckets() {
		if b.Key != BucketWaiting {
			out = append(out, b.Key)
		}
	}
	return out
}

// ---- schedule ----

func scheduleGroup(p Profile) string { return "gmail_triage:" + p.Name }

type storedSchedule struct {
	Times    []string `json:"times"`
	Days     string   `json:"days"`
	Timezone string   `json:"timezone"`
}

// Schedule returns the effective schedule and whether Telegram overrode it.
func (s *Service) Schedule(p Profile) (Schedule, bool, error) {
	raw, err := s.store.GetState(p.Name, stateSchedule)
	if err != nil || raw == "" {
		return p.Schedule, false, err
	}
	var st storedSchedule
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		log.Printf("[gmail_triage] stored schedule for %s is unreadable, using config: %v", p.Name, err)
		return p.Schedule, false, nil
	}
	sched, err := ParseSchedule(strings.Join(st.Times, ","), st.Days, st.Timezone)
	if err != nil {
		log.Printf("[gmail_triage] stored schedule for %s is invalid, using config: %v", p.Name, err)
		return p.Schedule, false, nil
	}
	return sched, true, nil
}

// SetSchedule stores a schedule override and re-registers cron entries.
// Empty days means every day; empty timezone keeps the current one.
func (s *Service) SetSchedule(p Profile, times, days, timezone string) (Schedule, error) {
	current, _, err := s.Schedule(p)
	if err != nil {
		return Schedule{}, err
	}
	if strings.TrimSpace(timezone) == "" {
		timezone = current.Timezone
	}
	sched, err := ParseSchedule(times, days, timezone)
	if err != nil {
		return Schedule{}, err
	}
	data, err := json.Marshal(storedSchedule{Times: sched.Times, Days: sched.DaysString(), Timezone: sched.Timezone})
	if err != nil {
		return Schedule{}, err
	}
	if err := s.store.SetState(p.Name, stateSchedule, string(data)); err != nil {
		return Schedule{}, err
	}
	return sched, s.reschedule(p)
}

// ResetSchedule drops the override and returns to the config schedule.
func (s *Service) ResetSchedule(p Profile) (Schedule, error) {
	if err := s.store.SetState(p.Name, stateSchedule, ""); err != nil {
		return Schedule{}, err
	}
	return p.Schedule, s.reschedule(p)
}

// Paused reports whether scheduled digests are off.
func (s *Service) Paused(p Profile) (bool, error) {
	v, err := s.store.GetState(p.Name, statePaused)
	return v == "1", err
}

// SetPaused turns scheduled digests off or on. /triage keeps working.
func (s *Service) SetPaused(p Profile, paused bool) error {
	value := ""
	if paused {
		value = "1"
	}
	if err := s.store.SetState(p.Name, statePaused, value); err != nil {
		return err
	}
	return s.reschedule(p)
}

// StartSchedules registers cron entries for every profile; fire runs a
// scheduled digest. Call once after the scheduler exists.
func (s *Service) StartSchedules(scheduler NativeScheduler, fire func(Profile)) error {
	s.schedMu.Lock()
	s.scheduler = scheduler
	s.fire = fire
	s.schedMu.Unlock()
	var errs []error
	for _, p := range s.profiles {
		if err := s.reschedule(p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Service) reschedule(p Profile) error {
	s.schedMu.Lock()
	scheduler, fire := s.scheduler, s.fire
	s.schedMu.Unlock()
	if scheduler == nil || fire == nil {
		return nil
	}
	paused, err := s.Paused(p)
	if err != nil {
		return err
	}
	var specs []string
	if !paused {
		sched, _, err := s.Schedule(p)
		if err != nil {
			return err
		}
		specs = sched.CronSpecs()
	}
	return scheduler.ReplaceNativeJobs(scheduleGroup(p), specs, func() { fire(p) })
}

// NextRun is the next scheduled digest (zero when paused or not scheduled).
func (s *Service) NextRun(p Profile) time.Time {
	s.schedMu.Lock()
	scheduler := s.scheduler
	s.schedMu.Unlock()
	if scheduler == nil {
		return time.Time{}
	}
	return scheduler.NativeNextRun(scheduleGroup(p))
}
