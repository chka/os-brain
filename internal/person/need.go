package person

import "errors"

// Need refuses up front for a command that needs a person, with the reason, rather than half
// way through. A terminal is where a person types, not proof that one is there: `script`
// gives any process one, which is why the process-tree check runs first.
func Need(what string) (TTY, error) {
	if LooksLikeAgent() {
		return nil, errors.New(what + " needs a person at a controlling terminal, and " +
			"this process runs under an agent's session or was taken out of one (a pty from " +
			"`script`, `setsid`, a double fork). Run it yourself, in a terminal you opened.")
	}
	t, err := openDevTTY()
	if err == nil && t == nil {
		err = ErrNoTTY
	}
	if err != nil {
		return nil, errors.New(what + " needs a person at a controlling terminal; " +
			"run it yourself in a terminal, not through an agent or a pipe (" + err.Error() + ").")
	}
	return t, nil
}
