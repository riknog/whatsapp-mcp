//go:build race

package ingest

import "time"

// historyBudget is zero under -race: the race detector slows SQLite by an amount
// that depends on machine load, so the time limit is only checked without it.
const historyBudget time.Duration = 0
