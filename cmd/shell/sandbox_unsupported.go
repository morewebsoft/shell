//go:build (!linux && !darwin && !windows) || (linux && !cgo)

package main

func isSandboxSupported() bool {
	return false
}

func isSigSys(err error) bool {
	return false
}

func runSandboxedChild(cmdStr string) {
	// No-op for unsupported platforms or Linux environments built without CGO
}