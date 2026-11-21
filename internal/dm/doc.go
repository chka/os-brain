// Package dm is the registry of live sessions and the direct messages between them, both kept
// in the spine so every project on the machine can reach every other. The commands, the hooks
// and the channel listener use it through the Messenger interface; dm_hook.go shapes what a
// hook prints so Claude Code reads it, and dm_command.go is `os-brain dm` and
// `os-brain sessions`.
package dm
