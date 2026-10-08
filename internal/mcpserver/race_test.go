//go:build race

package mcpserver

import "time"

// initializeBudget is zero under -race: the race detector slows the handshake
// by an amount that depends on machine load, so the time limit is only checked
// without it. The 5 s ceiling still catches a server that waits for WhatsApp.
const initializeBudget time.Duration = 0
