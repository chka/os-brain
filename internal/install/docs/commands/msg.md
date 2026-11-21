---
description: Send a brain direct message to another session or project
argument-hint: <session-id | project | project/id> <message>
allowed-tools: Bash(os-brain dm:*)
disable-model-invocation: true
---

Send a brain direct message.

The first token in the arguments below is the target — a session id-prefix, a project name,
or `project/id-prefix`. Everything after it is the message body. Run exactly:

    os-brain dm <target> - <<'BRAIN_DM_<TAG>'
    <the whole message, exactly as I wrote it>
    BRAIN_DM_<TAG>

<TAG> is 16 random hex characters you make up now, new for this send, and it must not
appear anywhere in the message; if it does, pick another. A fixed terminator lets a line of
the message that equals it end the heredoc, and every line after it would run as a command.
The body goes on stdin, never as a quoted argument: a message with quotes or newlines in it
breaks shell quoting, and a broken quote is how a stray half-message gets sent.

Send only what I wrote, verbatim; do not add anything to it. Then show me the result line —
which session it reached and when it will be read. If the target is ambiguous or you are not
sure it resolves, run `os-brain sessions` first and ask me which one before sending.

Arguments: $ARGUMENTS
