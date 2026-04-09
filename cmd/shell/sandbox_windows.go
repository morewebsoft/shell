//go:build windows

package main

import (
	"os"
	"os/exec"
	"regexp"
)

func isSandboxSupported() bool {
	return true
}

func isSigSys(err error) bool {
	if exitErr, ok := err.(*exec.ExitError); ok {
		// We use exit code 31 (matching SIGSYS) to emulate a kernel trap on Windows
		if exitErr.ExitCode() == 31 {
			return true
		}
	}
	return false
}

func runSandboxedChild(cmdStr string) {
	// Windows lacks an unprivileged kernel sandbox equivalent to Seccomp/Seatbelt.
	// As a best-practice application-layer alternative, we use static regex analysis 
	// to intercept destructive commands in this shim process before invoking cmd.exe.
	
	destructivePatterns := []string{
		`(?i)\bdel\b`,
		`(?i)\berase\b`,
		`(?i)\brmdir\b`,
		`(?i)\brd\b`,
		`(?i)\bRemove-Item\b`,
		`(?i)\bformat\b`,
	}

	for _, p := range destructivePatterns {
		if matched, _ := regexp.MatchString(p, cmdStr); matched {
			// Intercepted a destructive command!
			os.Exit(31) // Magic exit code to trigger the sandbox warning in parent
		}
	}

	cmd := exec.Command("cmd", "/c", cmdStr)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
	os.Exit(0)
}