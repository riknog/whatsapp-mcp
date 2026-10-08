//go:build !race

package ingest

import "time"

// historyBudget is the time limit for ingesting 1 000 history messages (T04: < 2 s).
const historyBudget = 2 * time.Second
