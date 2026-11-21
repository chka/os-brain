// Package install puts a brain into a project and takes it out again: init creates the store
// and brain.json, writes the agent docs and slash commands that travel inside the binary
// (docs/, embedded), and wires the session hooks of every agent host it finds; uninstall
// removes exactly what init added and nothing the project owned. The hook entries are
// encoded and recognised in this one package, so wiring and removal can never disagree about
// what the brain wrote.
package install
