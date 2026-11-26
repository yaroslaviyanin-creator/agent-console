package main

import "golang.org/x/sys/unix"

// CLI processes share the runner's UID. Block access to the server's initial
// /proc environment and process memory even if a CLI process is compromised.
func restrictProcessInspection() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}
