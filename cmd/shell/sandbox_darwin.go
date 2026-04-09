//go:build darwin

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func isSandboxSupported() bool {
	return true
}

func isSigSys(err error) bool {
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			// macOS Seatbelt sandbox-exec configured to send SIGKILL on violation
			if status.Signaled() && status.Signal() == syscall.SIGKILL {
				return true
			}
		}
	}
	return false
}

func runSandboxedChild(cmdStr string) {
	// macOS Seatbelt profile: allow everything, but kill on file deletion
	profile := "(version 1)\n(allow default)\n(deny file-write-unlink (with send-signal SIGKILL))\n(deny file-write-unlinked (with send-signal SIGKILL))"

	binary, err := exec.LookPath("sandbox-exec")
	if err != nil {
		os.Exit(1)
	}

	// Replaces current process with the sandboxed shell
	err = syscall.Exec(binary, []string{"sandbox-exec", "-p", profile, "sh", "-c", cmdStr}, os.Environ())
	if err != nil {
		os.Exit(1)
	}
}