//go:build windows

package person

import "os"

// Windows has no root-owned ps to trust, so the ps fallback is never used there.
func ownedByRoot(os.FileInfo) bool { return false }
