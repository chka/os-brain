// Package doctor checks a brain's health and reports what it finds: the schema and its
// integrity, the full-text index against the memories, supersede claims the store never
// recorded, measures that cannot be summarised, and the wiring the hooks depend on. Each
// finding names the command that clears it. With --fix it repairs what is safe to repair,
// after dumping the project first. Vacuum compacts the database file.
package doctor
