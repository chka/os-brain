// Package project holds the commands that act on a project as a whole rather than on one
// memory: context (what the brain knows about the project), the scope registry, relabel and
// absorb (moving rows from one project label to another, or from another brain), share
// (moving a project between its own brain.db and the shared store, and back) and private
// (what the project publishes to the shared layer). share moves the only copy of a project's
// memory, so it is a dry run until --apply and leaves a dump behind.
package project
