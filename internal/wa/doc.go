// Package wa wraps whatsmeow behind the Client interface. Together with
// internal/ingest it is the only code that imports whatsmeow; a test
// (deps_test.go) enforces that on direct imports. The rest of the program talks
// to Client, and tests use Fake.
//
// Events must be drained: Client.Events delivers translated events (see
// internal/ingest) in the order WhatsApp sent them, and the adapter does not drop
// them. If nobody reads, WhatsApp delivery blocks and a warning is logged after
// 10 s. Read Events until Close.
//
// Login pairs a device and must be called with a sink, which receives every
// event during the login (history and app state are sent once, so they are not
// dropped). The CLI passes a function that calls ingest.Ingestor.Handle, then
// closes the adapter. Connect and ingest.Run are for the serve command.
package wa
