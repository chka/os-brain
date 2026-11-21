// Package store is the brain's storage: one SQLite brain.db per project (or a shared one for
// several), and the per-user spine every project registers in. It owns the schema and its
// forward-only migrations, the compatibility check that stops an older client from writing
// a newer database, the binding of a project name to its directory, the memories table with
// its full-text index and archive, sessions and their cursors, brain.json, and the
// vocabulary of memory types, tiers and confidence that every command shares.
//
// Everything a command does is built on this package, and it knows about none of them: the
// features (remember, recall, the brief, ...) sit above it and reach each other only through
// the layering in internal/layers. The format it writes is the format ns-brain writes, so a
// brain moves between the editions with nothing lost.
package store
