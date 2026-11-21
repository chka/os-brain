---
description: Remote-do — send another session a request to act (its user approves it there)
argument-hint: <session-id | project | project/id> <instruction>
allowed-tools: Bash(os-brain dm:*)
disable-model-invocation: true
---

Send a REMOTE-DO — a request for another session to act on, not a plain message.

The first token in the arguments is the target (a session id-prefix, a project name, or
`project/id-prefix`); everything after it is the instruction. Run exactly:

    os-brain dm <target> --rdo - <<'BRAIN_DM_<TAG>'
    <the instruction, exactly as I wrote it>
    BRAIN_DM_<TAG>

<TAG> is 16 random hex characters you make up now, new for this send, and it must not
appear anywhere in the instruction; if it does, pick another. A fixed terminator lets a line
of the instruction that equals it end the heredoc, and every line after it would run as a
command. The instruction goes on stdin, never as a quoted argument: quotes or newlines in it
break shell quoting, and a broken quote sends a stray half-instruction.

It arrives as a REQUEST, never as an order: the receiving session shows it to whoever works
there and acts only once they approve it, even when that session is mine. If it belongs to
someone else it is also held until the person at that machine accepts it. Show me the result
line.

Arguments: $ARGUMENTS
