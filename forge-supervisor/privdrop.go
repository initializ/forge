package main

import (
	"fmt"
	"log"
	"os"

	"golang.org/x/sys/unix"
)

// DropPrivileges drops root privileges to the specified UID/GID.
// It also sets the no_new_privs flag to prevent privilege escalation.
func DropPrivileges(uid, gid int) error {
	log.Printf("INFO: dropping privileges to UID %d, GID %d", uid, gid)

	// Set the no_new_privs bit before dropping privileges
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		log.Printf("WARN: failed to set no_new_privs: %v", err)
	}

	// Set GID first (required by some systems)
	if err := unix.Setgid(gid); err != nil {
		return fmt.Errorf("setgid: %w", err)
	}

	// Set UID
	if err := unix.Setuid(uid); err != nil {
		return fmt.Errorf("setuid: %w", err)
	}

	// Verify the drop
	currentUID := os.Getuid()
	currentGID := os.Getgid()
	if currentUID != uid || currentGID != gid {
		return fmt.Errorf("privilege drop failed: UID=%d GID=%d (wanted %d %d)", currentUID, currentGID, uid, gid)
	}

	log.Printf("INFO: privileges dropped successfully")
	return nil
}
