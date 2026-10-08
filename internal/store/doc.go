// Package store owns the data.db SQLite schema, migrations, and repositories.
//
// Writes go through a single connection (MaxOpenConns(1)) and reads through a
// separate pool, both in WAL mode, so readers never block the writer. All
// times are Unix seconds. Methods take a context. Chat-scoped reads do not
// filter hidden chats; the tools must check Chat.Hidden first. Multi-chat reads
// (Search, NewInbound, ListChats without IncludeHidden) do filter them.
package store
