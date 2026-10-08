// Package ingest turns WhatsApp events into store rows.
//
// Translate converts whatsmeow events into the plain types declared in
// events.go, which carry JIDs as strings and no whatsmeow types. The wa package
// forwards those types on Client.Events. Ingestor.Run consumes them and writes
// to the store. Only this package and internal/wa import whatsmeow.
//
// Ingest never logs JIDs, phone numbers or message text.
package ingest
