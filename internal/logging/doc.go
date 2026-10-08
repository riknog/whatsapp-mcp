// Package logging provides slog-based logging to stderr and a rotating log file,
// with PII redaction. The handler here redacts every record, including the
// records of whatsmeow, whose adapter (internal/wa) logs through it.
//
// Nothing here writes to stdout, which belongs to the MCP transport.
package logging
