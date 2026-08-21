//go:build !windows

package builtin

import (
	"fmt"
	"os"
)

// replaceBinary renames the new binary over the running one.
//
// Safe here because a file is its inode: the running process keeps the one it
// already opened, and the name is simply repointed. The old inode goes away
// when the last process using it exits.
func replaceBinary(self, staged string) error {
	if err := os.Rename(staged, self); err != nil {
		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}

	return nil
}

// sweep has nothing to do on a system where the replacement leaves nothing
// behind: the old inode goes away on its own.
func sweep(string) error {
	return nil
}
