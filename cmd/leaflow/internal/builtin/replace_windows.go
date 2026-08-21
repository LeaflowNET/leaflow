//go:build windows

package builtin

import (
	"errors"
	"fmt"
	"os"
)

// suffix marks the binary this update displaced. It stays on disk until the
// next update, because the process holding it is the one doing the replacing.
const suffix = ".old"

// replaceBinary moves the running binary aside and puts the new one in its
// place.
//
// Windows has no inodes, and a running image is held open by the kernel without
// delete sharing. Renaming the new file over the old one is MoveFileEx with
// MOVEFILE_REPLACE_EXISTING, which has to delete the target first and cannot:
// it fails with a sharing violation, and `leaflow update` would report "not
// checkWritable" on a directory the user can plainly write to.
//
// What Windows does allow is renaming the running image itself — the handle
// follows the file, not the name. So the running binary is moved out of the way
// and the new one takes the name it vacated. The displaced file cannot be
// deleted while this process lives; it is swept on the next update instead of
// being left for the user to find.
func replaceBinary(self, staged string) error {
	displaced := self + suffix

	if err := os.Rename(self, displaced); err != nil {
		return fmt.Errorf("%w: cannot move the running binary aside: %v", ErrNotWritable, err)
	}

	if err := os.Rename(staged, self); err != nil {
		// Put it back. Failing here without this would leave the user with no
		// leaflow at all, which is worse than a failed update.
		if restored := os.Rename(displaced, self); restored != nil {
			return fmt.Errorf("%w: the update failed and %s could not be restored: %v",
				ErrNotWritable, self, errors.Join(err, restored))
		}

		return fmt.Errorf("%w: %v", ErrNotWritable, err)
	}

	return nil
}

// sweep removes what a previous update displaced.
//
// Called before an update rather than at every startup: only someone who has
// updated has one of these, and paying a file system call on every command to
// tidy up after a rare event is the wrong trade.
//
// A failure here is reported rather than swallowed. The file is removable once
// the process that was running it has exited, so one that will not go usually
// means another leaflow is still running — and that is the same thing that will
// make the update itself fail, said earlier and in plainer terms.
func sweep(self string) error {
	displaced := self + suffix

	if err := os.Remove(displaced); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s is left over from an earlier update and cannot be removed; "+
			"close any other running leaflow: %v", ErrNotWritable, displaced, err)
	}

	return nil
}
