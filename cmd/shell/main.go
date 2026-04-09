package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/TadB0x/IntelliShell/presets"

	"github.com/charmbracelet/huh"
)

type AppConfig struct {
	Provider string
	Model    string
	APIKey   string
}

// AIRegistry represents a dynamic list of providers and models
type AIRegistry struct {
	Providers []ProviderConfig `json:"providers"`
}

type ProviderConfig struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Models []string `json:"models"`
}

var (
	config   = AppConfig{
		Provider: "google",
		Model:    "gemini-1.5-flash",
		APIKey:   os.Getenv("GEMINI_API_KEY"),
	}

	colorCyan   = "\033[36m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorPurple = "\033[35m"
	colorReset  = "\033[0m"
	clearLine   = "\r\033[K"
)

func init() {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" || (runtime.GOOS == "windows" && os.Getenv("TERM") == "" && os.Getenv("WT_SESSION") == "") {
		colorCyan = ""
		colorGreen = ""
		colorYellow = ""
		colorRed = ""
		colorPurple = ""
		colorReset = ""
		clearLine = "\r" + strings.Repeat(" ", 80) + "\r"
	}
}

func main() {
	loadConfig()
	reader := bufio.NewReader(os.Stdin)
	ctx := context.Background()

	fmt.Println("Welcome to IntelliShell. Type natural language, native commands, '/model' for AI setup, '/settings' for preferences, or 'exit' to quit.")

	for {
		// The native terminal prompt
		cwd, _ := os.Getwd()
		fmt.Printf("%sai-shell %s>%s ", colorCyan, cwd, colorReset)

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

		// Handle the model config menu
		if strings.HasPrefix(input, "/model") {
			handleModelConfig(ctx)
			continue
		}

		// Check for a preset command first for performance and to bypass AI
		if command, found := presets.CheckForPreset(input); found {
			fmt.Printf("%s-> %s%s\n", colorGreen, command, colorReset) // Show the resolved command
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
					fmt.Print(clearLine)
					done <- true
					return
				default:
					fmt.Printf("\r%s%s Translating...%s", colorPurple, chars[i], colorReset)
					i = (i + 1) % len(chars)
					time.Sleep(100 * time.Millisecond)
				}
			}
		}()

		// 1. Send English text to real AI for translation
		command, isSafe := generateCommandFromAI(ctx, input, done)

		// 2. Safety Verification
		if !isSafe {
			fmt.Printf("%s⚠️  Command might be unsafe. Execute? (y/n):%s ", colorYellow, colorReset)
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

func handleModelConfig(ctx context.Context) {
	fmt.Printf("\n%sFetching latest AI providers and models...%s\n", colorCyan, colorReset)
	registry := fetchAIRegistry()

	var providerOptions []huh.Option[string]
	for _, prov := range registry.Providers {
		providerOptions = append(providerOptions, huh.NewOption(prov.Name, prov.ID))
	}

	p := config.Provider
	
	// Step 1: Provider Selection
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("AI Provider").
				Options(providerOptions...).
				Value(&p),
		),
	).Run()

	if err != nil {
		fmt.Printf("\n%sConfiguration cancelled:%s %v\n", colorRed, colorReset, err)
		return
	}

	// Step 2: Dynamic Model Options based on Provider
	var modelOptions []huh.Option[string]
	for _, prov := range registry.Providers {
		if prov.ID == p {
			for _, mod := range prov.Models {
				modelOptions = append(modelOptions, huh.NewOption(mod, mod))
			}
			break
		}
	}
	modelOptions = append(modelOptions, huh.NewOption("Custom (Type manually)", "custom"))

	m := config.Model
	k := config.APIKey

	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Model").
				Description("Type to search/filter models...").
				Height(10).
				Options(modelOptions...).
				Value(&m),
			huh.NewInput().
				Title("API Key").
				EchoMode(huh.EchoModePassword).
				Value(&k),
		),
	).Run()

	if err != nil {
		fmt.Printf("\n%sConfiguration cancelled:%s %v\n", colorRed, colorReset, err)
		return
	}
	
	if m == "custom" {
		m = ""
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Enter Custom Model Name").
					Value(&m),
			),
		).Run()
		if err != nil {
			fmt.Printf("\n%sConfiguration cancelled:%s %v\n", colorRed, colorReset, err)
			return
		}
	}

	config.Provider, config.Model, config.APIKey = p, m, k
	saveConfig()

	fmt.Printf("\n%sSuccessfully updated AI configuration to %s (%s).%s\n", colorGreen, config.Provider, config.Model, colorReset)
}

// fetchAIRegistry retrieves the native providers and dynamically fetches OpenRouter's list
func fetchAIRegistry() AIRegistry {
	registry := AIRegistry{
		Providers: []ProviderConfig{
			{ID: "google", Name: "Google", Models: []string{"gemini-1.5-flash", "gemini-1.5-pro", "gemini-1.0-pro"}},
			{ID: "openai", Name: "OpenAI", Models: []string{"gpt-4o", "gpt-4-turbo", "gpt-3.5-turbo", "gpt-4o-mini"}},
			{ID: "anthropic", Name: "Anthropic", Models: []string{"claude-3-5-sonnet-20240620", "claude-3-opus-20240229"}},
			{ID: "groq", Name: "Groq", Models: []string{"gpt-oss-120b", "gpt-oss-20b", "llama-3.3-70b-versatile", "llama-3.1-8b-instant", "llama-4-scout"}},
			{ID: "vertex", Name: "Vertex AI", Models: []string{"gemini-1.5-flash", "gemini-1.5-pro", "gemini-1.0-pro"}},
		},
	}

	client := &http.Client{Timeout: 5 * time.Second}
	// Fetching real-time models from OpenRouter (no API key required for this endpoint)
	resp, err := client.Get("https://openrouter.ai/api/v1/models")
	if err != nil || resp.StatusCode != 200 {
		return registry
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return registry
	}

	var orResp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &orResp); err != nil {
		return registry
	}

	var orModels []string
	dynamicModels := make(map[string][]string)

	for _, m := range orResp.Data {
		orModels = append(orModels, m.ID)

		// Use OpenRouter to dynamically discover native models
		parts := strings.SplitN(m.ID, "/", 2)
		if len(parts) == 2 {
			dynamicModels[parts[0]] = append(dynamicModels[parts[0]], parts[1])
		}
	}

	for i, prov := range registry.Providers {
		if models, ok := dynamicModels[prov.ID]; ok && len(models) > 0 {
			registry.Providers[i].Models = models
		}
	}

	if len(orModels) > 0 {
		registry.Providers = append(registry.Providers, ProviderConfig{
			ID:     "openrouter",
			Name:   "OpenRouter",
			Models: orModels,
		})
	}

	return registry
}

// generateCommandFromAI uses the configured AI API to translate natural language into commands
func generateCommandFromAI(ctx context.Context, input string, done chan bool) (string, bool) {
	var stopSpinnerOnce sync.Once
	stopSpinner := func() {
		stopSpinnerOnce.Do(func() {
			done <- true
			<-done // Wait for the spinner to finish erasing
		})
	}
	defer stopSpinner() // Ensure spinner channel is always resolved

	if config.APIKey == "" {
		stopSpinner()
		fmt.Printf("\r%sError: API key is not configured. Please use '/model' to set it.%s\n", colorRed, colorReset)
		return "", true
	}

	var baseURL string
	modelID := config.Model

	switch config.Provider {
	case "google":
		baseURL = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
	case "openai":
		baseURL = "https://api.openai.com/v1/chat/completions"
	case "groq":
		baseURL = "https://api.groq.com/openai/v1/chat/completions"
	case "openrouter":
		baseURL = "https://openrouter.ai/api/v1/chat/completions"
	default:
		// Use OpenRouter as the unified source to hit the latest API endpoints for any other provider
		baseURL = "https://openrouter.ai/api/v1/chat/completions"
		modelID = config.Provider + "/" + config.Model
	}

	cwd, _ := os.Getwd()
	prompt := fmt.Sprintf(`You are a lightweight AI shell assistant for %s. 
Translate the user's natural language into a valid %s terminal command.
The current working directory is: %s
If the input is already a valid command, return it as is.
Return ONLY the raw command. Do not wrap it in quotes, markdown, or JSON.
If the command is destructive or dangerous (e.g., delete, format, rmdir), prefix it EXACTLY with "UNSAFE: ".
Otherwise, just output the command directly.

User input: %s`, runtime.GOOS, runtime.GOOS, cwd, input)

	reqBody := map[string]interface{}{
		"model": modelID,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.1,
		"stream":      true,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		stopSpinner()
		fmt.Printf("\r%sError encoding JSON:%s %v\n", colorRed, colorReset, err)
		return "", true
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewReader(jsonData))
	if err != nil {
		stopSpinner()
		fmt.Printf("\r%sError creating request:%s %v\n", colorRed, colorReset, err)
		return "", true
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	if baseURL == "https://openrouter.ai/api/v1/chat/completions" {
		req.Header.Set("HTTP-Referer", "https://github.com/tadB0x/IntelliShell")
		req.Header.Set("X-Title", "IntelliShell")
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		stopSpinner()
		fmt.Printf("\r%sAPI Error:%s %v\n", colorRed, colorReset, err)
		return "", true
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		stopSpinner()
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("\r%sAPI Error (%d):%s %s\n", colorRed, resp.StatusCode, colorReset, string(body))
		return "", true
	}

	scanner := bufio.NewScanner(resp.Body)
	var fullResponse string
	firstChunk := true

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err == nil {
				if len(chunk.Choices) > 0 {
					content := chunk.Choices[0].Delta.Content
					if content != "" {
						if firstChunk {
							stopSpinner()
							fmt.Print(colorGreen + "-> ")
							firstChunk = false
						}
						fmt.Print(content)
						fullResponse += content
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		stopSpinner()
		fmt.Printf("\r%sStream Error:%s %v\n", colorRed, colorReset, err)
		return "", true
	}

	if firstChunk {
		stopSpinner()
	} else {
		fmt.Println(colorReset) // Reset color and finalize newline
	}

	fullResponse = strings.TrimSpace(fullResponse)
	isSafe := true
	if strings.Contains(fullResponse, "UNSAFE:") {
		isSafe = false
	}

	if strings.Contains(fullResponse, "```") {
		firstIdx := strings.Index(fullResponse, "```")
		lastIdx := strings.LastIndex(fullResponse, "```")
		if firstIdx != -1 && lastIdx != -1 && firstIdx+3 <= lastIdx {
			block := fullResponse[firstIdx+3 : lastIdx]
			block = strings.TrimSpace(block)
			lines := strings.SplitN(block, "\n", 2)
			if len(lines) == 2 {
				firstLine := strings.TrimSpace(strings.ToLower(lines[0]))
				if firstLine == "bash" || firstLine == "sh" || firstLine == "shell" || firstLine == "cmd" || firstLine == "powershell" {
					block = strings.TrimSpace(lines[1])
				}
			} else if len(lines) == 1 {
				firstLine := strings.TrimSpace(strings.ToLower(lines[0]))
				if firstLine == "bash" || firstLine == "sh" || firstLine == "shell" || firstLine == "cmd" || firstLine == "powershell" {
					block = ""
				}
			}
			fullResponse = block
		}
	}

	fullResponse = strings.TrimPrefix(fullResponse, "UNSAFE: ")
	fullResponse = strings.TrimPrefix(fullResponse, "UNSAFE:")
	fullResponse = strings.TrimPrefix(fullResponse, "```bash")
	fullResponse = strings.TrimPrefix(fullResponse, "```sh")
	fullResponse = strings.TrimPrefix(fullResponse, "```")
	fullResponse = strings.TrimSuffix(fullResponse, "```")
	fullResponse = strings.TrimSpace(fullResponse)

	return fullResponse, isSafe
}

func executeCommand(cmdStr string) {
	if cmdStr == "" {
		return
	}

	// Internal handling for "cd" to preserve directory state within the Go application
	if strings.HasPrefix(cmdStr, "cd ") || cmdStr == "cd" || strings.HasPrefix(cmdStr, "cd..") {
		dir := strings.TrimSpace(strings.TrimPrefix(cmdStr, "cd"))
		if dir == ".." {
			dir = ".."
		} else if dir == "" {
			if runtime.GOOS == "windows" {
				cwd, _ := os.Getwd()
				fmt.Println(cwd)
			} else {
				home, _ := os.UserHomeDir()
				_ = os.Chdir(home)
			}
			return
		}
		
		dir = strings.Trim(dir, "\"'") // Remove quotes if any
		if err := os.Chdir(dir); err != nil {
			fmt.Printf("%scd failed:%s %v\n", colorRed, colorReset, err)
		}
		return
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", cmdStr)
	} else {
		// Try to use bash for better compatibility with AI-generated commands, fallback to sh
		shell := "sh"
		if _, err := exec.LookPath("bash"); err == nil {
			shell = "bash"
		}
		cmd = exec.Command(shell, "-c", cmdStr)
	}
	
	// Bind standard streams so the output behaves exactly like a native terminal
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	if err != nil {
		fmt.Printf("%sCommand failed:%s %v\n", colorRed, colorReset, err)
	}
}
func getConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.json"
	}
	configDir := filepath.Join(home, ".intellishell")
	_ = os.MkdirAll(configDir, 0755)
	return filepath.Join(configDir, "config.json")
}

func loadConfig() {
	path := getConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &config)
}

func saveConfig() {
	path := getConfigPath()
	data, err := json.MarshalIndent(config, "", "  ")
	if err == nil {
		_ = os.WriteFile(path, data, 0644)
	}
}
