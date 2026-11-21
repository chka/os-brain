// Package fsguard is every file the brain writes or reads outside its own database: the
// refusal to write outside the project or into a git-tracked path, the refusal to follow a
// symlink, atomic private writes (WriteFileSafe, Dump), the read guard for files a command
// was pointed at, and the one sanctioned way to read from git. A brain holds memories that
// were never meant to be committed or shared, so an export, a snapshot or a backup that
// lands in the wrong place leaks them; these checks run before any byte is written.
package fsguard
