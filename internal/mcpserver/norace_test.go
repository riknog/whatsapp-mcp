//go:build !race

package mcpserver

import "time"

// initializeBudget is the time limit for answering initialize while WhatsApp is
// unreachable.
const initializeBudget = 500 * time.Millisecond
