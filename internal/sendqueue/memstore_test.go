package sendqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"

	"github.com/riknog/whatsapp-mcp/internal/clock"
	"github.com/riknog/whatsapp-mcp/internal/store"
)

// memStore is an in-memory Store with the same status rules as the real store.
type memStore struct {
	mu     sync.Mutex
	clk    clock.Clock
	items  []*store.SendItem // index = id-1
	sentAt map[int64]int64
	audits []memAudit
	sent   []memSent
}

type memSent struct {
	ref string
	msg store.Message
}

type memAudit struct{ action, ref, detail string }

func newMemStore(clk clock.Clock) *memStore {
	return &memStore{clk: clk, sentAt: map[int64]int64{}}
}

func (m *memStore) now() int64 { return m.clk.Now().Unix() }

func (m *memStore) get(id int64) *store.SendItem {
	if id < 1 || int(id) > len(m.items) {
		return nil
	}
	return m.items[id-1]
}

func (m *memStore) EnqueueSend(_ context.Context, it store.SendItem) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if it.Kind == store.KindContact {
		sum := sha256.Sum256([]byte("contact:" + it.SharedJID))
		it.TextHash = hex.EncodeToString(sum[:])
	} else {
		it.TextHash = store.TextHash(it.Text)
	}
	it.ID = int64(len(m.items) + 1)
	it.Status = store.StatusQueued
	it.EnqueuedAt = m.now()
	cp := it
	m.items = append(m.items, &cp)
	return it.ID, nil
}

func (m *memStore) NextQueued(context.Context) (store.SendItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range m.items {
		if it.Status == store.StatusQueued {
			return *it, nil
		}
	}
	return store.SendItem{}, store.ErrNotFound
}

func (m *memStore) GetSend(id int64) (store.SendItem, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it := m.get(id)
	if it == nil {
		return store.SendItem{}, false
	}
	return *it, true
}

func (m *memStore) CountPending(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, it := range m.items {
		if it.Status == store.StatusQueued || it.Status == store.StatusSending {
			n++
		}
	}
	return n, nil
}

func (m *memStore) transition(id int64, from []string, to string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	it := m.get(id)
	if it == nil {
		return store.ErrNotFound
	}
	for _, f := range from {
		if it.Status == f {
			it.Status = to
			return nil
		}
	}
	return store.ErrInvalidState
}

func (m *memStore) MarkSending(_ context.Context, id int64) error {
	return m.transition(id, []string{store.StatusQueued}, store.StatusSending)
}

func (m *memStore) MarkSent(_ context.Context, id int64, waID string) error {
	if err := m.transition(id, []string{store.StatusSending}, store.StatusSent); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.get(id).WAMessageID = waID
	m.sentAt[id] = m.now()
	return nil
}

func (m *memStore) MarkFailed(_ context.Context, id int64, reason string) error {
	if err := m.transition(id, []string{store.StatusQueued, store.StatusSending}, store.StatusFailed); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.get(id).Error = reason
	return nil
}

func (m *memStore) ExpireStale(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, it := range m.items {
		if it.Status == store.StatusQueued || it.Status == store.StatusSending {
			it.Status = store.StatusExpired
			n++
		}
	}
	return n, nil
}

func (m *memStore) SentSince(_ context.Context, since int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, t := range m.sentAt {
		if t >= since && m.get(id).Status == store.StatusSent {
			n++
		}
	}
	return n, nil
}

func (m *memStore) DistinctRecipientsSince(_ context.Context, since int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for id, t := range m.sentAt {
		if t >= since && m.get(id).Status == store.StatusSent {
			seen[m.get(id).ChatJID] = true
		}
	}
	return int64(len(seen)), nil
}

func (m *memStore) RecentDuplicate(_ context.Context, chat, hash string, since int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range m.items {
		if it.ChatJID == chat && it.TextHash == hash && it.EnqueuedAt >= since {
			switch it.Status {
			case store.StatusQueued, store.StatusSending, store.StatusSent:
				return true, nil
			}
		}
	}
	return false, nil
}

func (m *memStore) Audit(_ context.Context, action, ref, detail string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits = append(m.audits, memAudit{action, ref, detail})
	return nil
}

func (m *memStore) Audits() []memAudit {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]memAudit(nil), m.audits...)
}

// Statuses returns the status of every item in id order.
func (m *memStore) Statuses() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.items))
	for _, it := range m.items {
		out = append(out, it.Status)
	}
	return out
}

var _ Store = (*memStore)(nil)

func (m *memStore) RecordSent(_ context.Context, ref string, msg store.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, memSent{ref, msg})
	return nil
}

func (m *memStore) sentMessages() []memSent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]memSent(nil), m.sent...)
}
