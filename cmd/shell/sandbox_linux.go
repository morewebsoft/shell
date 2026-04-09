//go:build linux && cgo

package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	seccomp "github.com/seccomp/libseccomp-golang"
)

// isSandboxSupported returns true natively on Linux
func isSandboxSupported() bool {
	return true
}

// isSigSys checks if the command was explicitly killed by our seccomp kernel trap
func isSigSys(err error) bool {
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			// SIGSYS (Signal 31) indicates a blocked system call
			if status.Signaled() && status.Signal() == syscall.SIGSYS {
				return true
			}
		}
	}
	return false
}

// runSandboxedChild applies seccomp filters and then replaces the process with the actual command
func runSandboxedChild(cmdStr string) {
	// Create a default ALLOW filter
	filter, err := seccomp.NewFilter(seccomp.ActAllow)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating seccomp filter: %v\n", err)
		os.Exit(1)
	}

	// List of destructive syscalls we want to intercept
	syscallsToBlock := []string{
		"unlink", "unlinkat", // Delete files
		"rmdir",              // Delete directories
		"rename", "renameat", "renameat2", // Often used in file overwrites
	}

	for _, call := range syscallsToBlock {
		callID, err := seccomp.GetSyscallFromName(call)
		if err == nil {
			// ActTrap explicitly raises SIGSYS which we can detect gracefully in the parent
			filter.AddRule(callID, seccomp.ActTrap)
		}
	}

	if err := filter.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Error loading seccomp filter: %v\n", err)
		os.Exit(1)
	}

	shell := "sh"
	if _, err := exec.LookPath("bash"); err == nil {
		shell = "bash"
	}

	// Execute the shell command (replaces the current process entirely)
	binary, _ := exec.LookPath(shell)
	err = syscall.Exec(binary, []string{shell, "-c", cmdStr}, os.Environ())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Sandbox exec failed: %v\n", err)
		os.Exit(1)
	}
}