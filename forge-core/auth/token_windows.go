//go:build windows

package auth

import (
	"fmt"
	"os/exec"
	"os/user"
)

// setFileOwnerOnly restricts the token file to the current user on Windows
// using icacls to remove inherited permissions and grant only the owner.
func setFileOwnerOnly(path string) error {
	u, err := user.Current()
	if err != nil {
		return fmt.Errorf("getting current user: %w", err)
	}

	// Remove inherited permissions.
	if out, err := exec.Command("icacls", path, "/inheritance:r").CombinedOutput(); err != nil {
		return fmt.Errorf("removing inheritance: %s: %w", out, err)
	}

	// Grant full control to the current user only.
	if out, err := exec.Command("icacls", path, "/grant:r", u.Username+":F").CombinedOutput(); err != nil {
		return fmt.Errorf("granting owner access: %s: %w", out, err)
	}

	return nil
}
