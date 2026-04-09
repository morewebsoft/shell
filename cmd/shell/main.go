package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	// Assumes a `go.mod` file defines the module name (e.g., 'shell')
	"shell/presets"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

func main() {
	reader := bufio.NewReader(os.Stdin)
	ctx := context.Background()

	fmt.Println("Welcome to AI Shell. Type natural language, native commands, '/settings' for preferences, or 'exit' to quit.")

	for {
		// The native terminal prompt
		fmt.Print("\033[36mai-shell>\033[0m ")

		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error reading input:", err)
			break
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		if strings.ToLower(input) == "exit" {
			break
		}

		// Handle the settings menu
		if strings.HasPrefix(input, "/settings") {
			handleSettings()
			continue
		}

		// Check for a preset command first for performance and to bypass AI
		if command, found := presets.CheckForPreset(input); found {
			fmt.Printf("\033[32m-> %s\033[0m\n", command) // Show the resolved command
			// Presets are considered safe and are executed directly
			executeCommand(command)
			continue // Skip AI and go to next prompt
		}

		// Show a premium loading spinner while waiting for the AI
		done := make(chan bool)
		go func() {
			chars := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
			i := 0
			for {
				select {
				case <-done:
					// Clear the spinner line completely
					fmt.Print("\r\033[K")
					return
				default:
					fmt.Printf("\r\033[35m%s Translating...\033[0m", chars[i])
					i = (i + 1) % len(chars)
					time.Sleep(100 * time.Millisecond)
				}
			}
		}()

		// 1. Send English text to real AI for translation
		command, isSafe := generateCommandFromAI(ctx, input)

		// Stop the spinner
		done <- true

		// Visually display the generated command to simulate the "replacement" feel
		fmt.Printf("\033[32m-> %s\033[0m\n", command)

		// 2. Safety Verification
		if !isSafe {
			fmt.Print("\033[33m⚠️  Command might be unsafe. Execute? (y/n):\033[0m ")
			confirm, _ := reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(confirm)) != "y" {
				fmt.Println("Execution cancelled.")
				continue
			}
		}

		// 3. Execution
		executeCommand(command)
	}
}

func handleSettings() {
	fmt.Println("\n--- ⚙️ Settings ---")
	fmt.Println("1. Auto-execution: ON (Ask only on unsafe)")
	fmt.Println("2. AI API Configuration (Not configured)")
	fmt.Println("3. Mode: English + Native integrated")
	fmt.Println("(Note: Settings interactive menu to be implemented)\n")
}

// generateCommandFromAI uses the official Gemini API to translate natural language into commands
func generateCommandFromAI(ctx context.Context, input string) (string, bool) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return "echo \033[31mError: GEMINI_API_KEY environment variable is not set.\033[0m", true
	}

	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return fmt.Sprintf("echo Error creating AI client: %v", err), true
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-1.5-flash")
	
	prompt := fmt.Sprintf(`You are a lightweight AI shell assistant for Windows. 
Translate the user's natural language into a valid Windows cmd command.
If the input is already a valid command, return it as is.
Return ONLY a raw JSON object. Do not wrap it in markdown block quotes.
Format: {"command": "the command to run", "is_safe": true/false}
Set is_safe to false ONLY for destructive/dangerous commands (e.g., delete, format, rmdir).

User input: %s`, input)

	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil || len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "echo \033[31mAI Error: unable to generate a response\033[0m", true
	}

	textResponse, ok := resp.Candidates[0].Content.Parts[0].(genai.Text)
	if !ok {
		return "echo \033[31mAI Error: unexpected response format\033[0m", true
	}

	// Clean potential markdown codeblock formatting that the AI might forcefully inject
	cleanJSON := strings.TrimPrefix(strings.TrimSpace(string(textResponse)), "```json")
	cleanJSON = strings.TrimPrefix(cleanJSON, "```")
	cleanJSON = strings.TrimSuffix(cleanJSON, "```")

	var aiResp struct {
		Command string `json:"command"`
		IsSafe  bool   `json:"is_safe"`
	}

	if err := json.Unmarshal([]byte(cleanJSON), &aiResp); err != nil {
		return fmt.Sprintf("echo \033[31mError parsing AI JSON response:\033[0m %v", err), false
	}

	return aiResp.Command, aiResp.IsSafe
}

func executeCommand(cmdStr string) {
	if cmdStr == "" {
		return
	}

	// Assuming a Windows environment based on the file path.
	// We wrap execution in 'cmd /c' so built-in commands like 'dir', 'cd', 'echo' work seamlessly.
	cmd := exec.Command("cmd", "/c", cmdStr)
	
	// Bind standard streams so the output behaves exactly like a native terminal
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	if err != nil {
		fmt.Printf("\033[31mCommand failed:\033[0m %v\n", err)
	}
}