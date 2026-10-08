package sendqueue

import (
	"context"

	"github.com/riknog/whatsapp-mcp/internal/store"
)

// Store is the subset of *store.Store that the queue uses. It is declared here
// so that the queue can be tested with an in-memory fake; the compile-time
// check below keeps it in step with the real store.
type Store interface {
	EnqueueSend(ctx context.Context, it store.SendItem) (int64, error)
	NextQueued(ctx context.Context) (store.SendItem, error)
	CountPending(ctx context.Context) (int64, error)
	MarkSending(ctx context.Context, id int64) error
	MarkSent(ctx context.Context, id int64, waMessageID string) error
	MarkFailed(ctx context.Context, id int64, reason string) error
	ExpireStale(ctx context.Context) (int64, error)
	SentSince(ctx context.Context, since int64) (int64, error)
	DistinctRecipientsSince(ctx context.Context, since int64) (int64, error)
	RecentDuplicate(ctx context.Context, chatJID, textHash string, since int64) (bool, error)
	Audit(ctx context.Context, action, chatRef, detail string) error
	RecordSent(ctx context.Context, chatRef string, m store.Message) error
}

var _ Store = (*store.Store)(nil)
