// Package mcpserver is the MCP face of whatsapp-mcp: it registers the tools and
// the prompt of docs/02-TOOLS.md on a go-sdk server, validates their input,
// applies the hard limits, and formats their output.
//
// Every output goes through present.go, the single presentation layer. It
// turns store rows into the Message and chat shapes of the spec, redacts text
// with privacy.RedactText, and names people with identity.DisplayName. Tool
// output never carries a phone number or a JID; the no-PII test in
// nopii_test.go runs every tool and checks the output with privacy.AssertNoPII.
//
// Errors are toolerr.Error values. They reach the model as an output with
// "error" set and isError true, never as a protocol error.
//
// serve.go wires the real runtime (config, store, WhatsApp adapter, ingest,
// retention, send queue) around New and runs it on the stdio transport.
package mcpserver
