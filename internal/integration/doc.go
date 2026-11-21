// Package integration holds no code, only tests that drive several packages at once: a
// memory remembered through internal/memory and then read back through recall, the brief or
// an export; a project installed by internal/install and then uninstalled with its store
// still in place; two projects sharing one store and each command staying inside what its
// project may see. Each feature package tests itself, and only from below; these tests
// check the guarantees that only hold when the features run together, the way the commands
// run them.
//
// The tests are an external package (integration_test) and run under testguard, like every
// other package's tests, so no run writes into a real brain.
package integration
