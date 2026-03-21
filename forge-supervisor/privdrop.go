package main

import (
	"fmt"
	"log"
	"os"

	"golang.org/x/sys/unix"
)

// DropPrivileges drops root privileges to the specified UID/GID.
// It clears all supplementary groups, drops all capabilities,
// and sets PR_SET_NO_NEW_PRIVS to prevent privilege escalation.
func DropPrivileges(uid, gid int) error {
	log.Printf("INFO: dropping privileges to UID %d, GID %d", uid, gid)

	// Clear supplementary groups before setgid (required on some systems)
	if err := unix.Setgroups([]int{gid}); err != nil {
		log.Printf("WARN: Setgroups: %v (continuing)", err)
	}

	// Set no_new_privs before dropping privileges
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		log.Printf("WARN: PR_SET_NO_NEW_PRIVS: %v", err)
	}

	// Drop all capabilities (bounding set)
	if err := dropAllCapabilities(); err != nil {
		log.Printf("WARN: drop capabilities: %v", err)
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

// dropAllCapabilities clears the capability bounding set.
func dropAllCapabilities() error {
	// Clear the capability bounding set (limits what can be raised)
	for cap := 0; cap <= 40; cap++ {
		unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(cap), 0, 0, 0)
	}
	return nil
}
