package presets

import (
	"regexp"
	"runtime"
)

// CommandFamily groups multiple regex aliases to a single cross-platform command.
type CommandFamily struct {
	// A list of all regex patterns that trigger this command family.
	Aliases []*regexp.Regexp
	// A map where the key is the OS (e.g., "windows", "linux") and the value is the command string.
	Commands map[string]string
}

// commandFamilies holds the list of predefined command families that bypass the AI.
// This list is initialized once at startup for maximum performance.
var commandFamilies []CommandFamily

func init() {
	// This function runs once when the package is initialized.
	// We pre-compile all regexes here for performance.
	commandFamilies = []CommandFamily{
		{
			// Logical Action: List files and directories.
			Aliases: []*regexp.Regexp{
				regexp.MustCompile(`(?i)^(list|show|display)\s+files?$`),
				regexp.MustCompile(`(?i)^ls$`), // Common Unix alias
			},
			Commands: map[string]string{
				"windows": "dir",
				"linux":   "ls -la",
				"darwin":  "ls -la", // macOS
			},
		},
		{
			// Logical Action: Show the current working directory.
			Aliases: []*regexp.Regexp{
				regexp.MustCompile(`(?i)^(pwd|show\s+current\s+directory)$`),
			},
			Commands: map[string]string{
				"windows": "cd", // `cd` with no args shows the current directory on Windows
				"linux":   "pwd",
				"darwin":  "pwd",
			},
		},
		// --- Community contributions start here ---
		// Add more command families below. Group similar aliases together.
	}
}

// CheckForPreset iterates through the command families and returns a platform-specific command if a match is found.
// It returns the resolved command and a boolean indicating if a preset was matched.
func CheckForPreset(input string) (string, bool) {
	// Get the current operating system.
	currentOS := runtime.GOOS

	for _, family := range commandFamilies {
		for _, aliasRegex := range family.Aliases {
			if aliasRegex.MatchString(input) {
				// We found a matching alias. Now, get the command for the current OS.
				if cmd, ok := family.Commands[currentOS]; ok {
					// A command for the current OS is defined.
					return cmd, true
				}
				// If no command is defined for this OS, we can just fall through
				// and let the AI handle it.
				break // Move to the next family, no need to check other aliases in this one.
			}
		}
	}
	return "", false
}