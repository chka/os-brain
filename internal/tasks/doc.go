// Package tasks keeps a project's to-do list beside its memories: open, waiting, done and
// dropped tasks, one-off or on a schedule read by internal/schedule, and the due block the
// brief, the session start and the prompt hook show when a task falls due. task_rules.go
// holds which flags each subcommand takes, so the command line and the MCP server refuse the
// same combinations.
package tasks
