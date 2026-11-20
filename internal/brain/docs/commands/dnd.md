---
description: Do-not-disturb — mute this session, or (with "all") you on every machine
argument-hint: [on | off | all | all off]
allowed-tools: Bash(os-brain dm:*)
disable-model-invocation: true
---

Toggle do-not-disturb. Read the arguments and run the matching command:

- empty, or `on`      → `os-brain dm --mute`
- `off`               → `os-brain dm --unmute`
- `all`, or `all on`  → `os-brain dm --mute --everywhere`
- `all off`           → `os-brain dm --unmute --everywhere`

While muted, plain messages wait in your inbox (`/inbox`) instead of interrupting, and a
remote-do still surfaces because it is waiting on you. Show me the result line.

Arguments: $ARGUMENTS
