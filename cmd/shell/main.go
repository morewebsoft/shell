package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/TadB0x/IntelliShell/presets"

	"github.com/chzyer/readline"
	"github.com/charmbracelet/huh"
)

type AppConfig struct {
	Provider    string
	Model       string
	APIKey      string
	ProxyURL    string
	AutoExecute bool
	AIMemory    string
	EnableHistory          bool
	EnableSessionMemory    bool
	EnablePersistentMemory bool
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

// HistoryEntry stores a single interaction for AI context memory.
type HistoryEntry struct {
	UserInput string
	Command   string
}

var sessionHistory []HistoryEntry
const maxHistorySize = 5
var sessionAIMemory string

var (
	config   = AppConfig{
		Provider:               "google",
		Model:                  "gemini-1.5-flash",
		APIKey:                 os.Getenv("GEMINI_API_KEY"),
		EnableHistory:          true,
		EnableSessionMemory:    true,
		EnablePersistentMemory: false, // Balanced setting: persistent memory off by default
	}

	colorCyan   = "\033[36m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorPurple = "\033[35m"
	colorGrey   = "\033[90m"
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
		colorGrey = ""
		colorReset = ""
		clearLine = "\r" + strings.Repeat(" ", 80) + "\r"
	}
}

type commandPainter struct{}

func (p *commandPainter) Paint(line []rune, pos int) []rune {
	s := string(line)
	if !strings.HasPrefix(s, "/") || strings.Contains(s, " ") || colorGrey == "" {
		return line
	}

	cmds := []string{"/setup", "/settings", "/version", "/help"}
	var matches []string
	for _, cmd := range cmds {
		if strings.HasPrefix(cmd, s) {
			matches = append(matches, cmd)
		}
	}

	// Hide the suggestion if the user has fully typed the command
	if len(matches) == 1 && matches[0] == s {
		return line
	}

	if len(matches) > 0 {
		// Get the remaining part of the first match to act as true inline ghost text
		suggestion := matches[0][len(s):]
		ghost := fmt.Sprintf("%s%s%s", colorGrey, suggestion, colorReset)
		
		res := make([]rune, len(line))
		copy(res, line)
		res = append(res, []rune(ghost)...)
		return res
	}

	return line
}

func addToHistory(input, command string) {
	sessionHistory = append(sessionHistory, HistoryEntry{UserInput: input, Command: command})
	if len(sessionHistory) > maxHistorySize {
		sessionHistory = sessionHistory[1:] // Keep only the last N items
	}
}

func main() {
	// Hidden entrypoint for the seccomp sandbox process
	if len(os.Args) > 2 && os.Args[1] == "__sandbox_exec" {
		runSandboxedChild(os.Args[2])
		return
	}

	loadConfig()
	ctx := context.Background()

	// Configure autocomplete for / commands
	completer := readline.NewPrefixCompleter(
		readline.PcItem("/setup"),
		readline.PcItem("/settings"),
		readline.PcItem("/version"),
		readline.PcItem("/help"),
		readline.PcItem("exit"),
	)

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          fmt.Sprintf("%sAI %s>%s ", colorCyan, "[PWD]", colorReset),
		AutoComplete:    completer,
		InterruptPrompt: "^C",
		EOFPrompt:       "exit",
		Painter:         &commandPainter{},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error initializing readline:", err)
		return
	}
	defer rl.Close()

	fmt.Println("Welcome to IntelliShell. Type natural language, native commands, '/setup' (or '/settings') for configuration, or 'exit' to quit.")

	for {
		cwd, _ := os.Getwd()
		rl.SetPrompt(fmt.Sprintf("%sAI %s>%s ", colorCyan, cwd, colorReset))

		line, err := rl.Readline()
		if err != nil { // Handle Ctrl+C or Ctrl+D
			break
		}

		input := strings.TrimSpace(line)
		if input == "" {
			continue
		}

		if strings.ToLower(input) == "exit" {
			break
		}

		// Handle the setup menu
		if strings.HasPrefix(input, "/setup") || strings.HasPrefix(input, "/settings") {
			handleSetup()
			continue
		}

		// Handle the version command
		if strings.HasPrefix(input, "/version") {
			fmt.Printf("%sIntelliShell version 0.1%s\n", colorCyan, colorReset)
			continue
		}

		// Handle the help command
		if strings.HasPrefix(input, "/help") {
			fmt.Printf("\n%s🧠 IntelliShell Help%s\n", colorCyan, colorReset)
			fmt.Printf("%s======================%s\n", colorGrey, colorReset)
			fmt.Printf("  %s<natural language>%s : Describe your task (e.g., 'find large files', 'undo last commit')\n", colorYellow, colorReset)
			fmt.Printf("  %s<native command>%s   : Run standard terminal commands (e.g., 'ls', 'cd', 'git status')\n", colorYellow, colorReset)
			fmt.Printf("  %s/setup%s (or %s/settings%s) : Configure AI providers, models, and execution preferences\n", colorGreen, colorReset, colorGreen, colorReset)
			fmt.Printf("  %s/version%s           : Display the current version of IntelliShell\n", colorGreen, colorReset)
			fmt.Printf("  %s/help%s              : Show this help menu\n", colorGreen, colorReset)
			fmt.Printf("  %sexit%s               : Quit the application\n\n", colorRed, colorReset)
			continue
		}

		// Check for a preset command first for performance and to bypass AI
		if command, found := presets.CheckForPreset(input); found {
			fmt.Printf("%s-> %s%s\n", colorGreen, command, colorReset) // Show the resolved command
			// Presets are considered safe and are executed directly
			executeCommand(command, true, rl)
			addToHistory(input, command)
			continue // Skip AI and go to next prompt
		}

		// Check for cross-platform command translations
		if command, found := presets.CheckForCrossPlatform(input); found {
			if command != input {
				fmt.Printf("%s-> %s (cross-platform)%s\n", colorGreen, command, colorReset)
			}
			executeCommand(command, true, rl)
			addToHistory(input, command)
			continue
		}

		// Solidify native command detection to bypass AI for immediate execution
		if isNativeCommand(input) {
			// Ensure native commands are still evaluated by the sandbox by passing false to forceUnsandboxed
			executeCommand(input, false, rl)
			addToHistory(input, input)
			continue
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
		needsExplicitConfirm := !isSafe || !config.AutoExecute
		if needsExplicitConfirm {
			msg := "Command might be unsafe."
			if config.AutoExecute && !isSafe {
				msg = "Command is UNSAFE."
			} else if !config.AutoExecute {
				msg = "Auto-execution is OFF."
			}

			fmt.Printf("%s%s Execute? (y/n):%s ", colorYellow, msg, colorReset)
			
			// We need a clean read here, bypassing readline temporary to avoid prompt collision
			confirmLine, err := rl.ReadlineWithDefault("")
			if err != nil {
				continue
			}
			if strings.ToLower(strings.TrimSpace(confirmLine)) != "y" {
				fmt.Println("Execution cancelled.")
				continue
			}
		}

		// 3. Execution
		executeCommand(command, needsExplicitConfirm, rl)
		addToHistory(input, command)
	}
}

func handleSetup() {
	for {
		// Prepare status indicators
		apiStatus := "Not Configured"
		if config.APIKey != "" || config.Provider == "ollama" || config.Provider == "lmstudio" {
			apiStatus = fmt.Sprintf("Configured - %s", config.Provider)
		}

		autoExecStatus := "OFF - Always Ask"
		if config.AutoExecute {
			autoExecStatus = "ON - Ask only on unsafe"
		}

		var category string
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Description("Choose a category to modify:").
					Options(
						huh.NewOption("AI Configuration ("+apiStatus+")", "ai"),
						huh.NewOption("Execution ("+autoExecStatus+")", "exec"),
						huh.NewOption("Context & Memory", "context"),
						huh.NewOption("Network / Proxy", "network"),
						huh.NewOption("Back to Shell", "exit"),
					).
					Value(&category),
			),
		).Run()

		if err != nil || category == "exit" || category == "" {
			return
		}

		switch category {
		case "ai":
			var aiAction string
			err := huh.NewForm(
				huh.NewGroup(
					huh.NewSelect[string]().
						Title("AI Configuration").
						Options(
							huh.NewOption("Update Model / API Key", "update"),
							huh.NewOption("Show Current Config", "show"),
							huh.NewOption("Back", "back"),
						).
						Value(&aiAction),
				),
			).Run()

			if err == nil && aiAction == "update" {
				handleModelConfig(context.Background())
			} else if err == nil && aiAction == "show" {
				keyPreview := "Not set"
				if len(config.APIKey) > 8 {
					keyPreview = config.APIKey[:4] + "..." + config.APIKey[len(config.APIKey)-4:]
				} else if config.APIKey != "" {
					keyPreview = "****"
				} else if config.Provider == "ollama" || config.Provider == "lmstudio" {
					keyPreview = "Not required for local providers"
				}
				fmt.Printf("\n%sCurrent AI Config:%s\n• Provider: %s\n• Model: %s\n• API Key: %s\n\n", colorCyan, colorReset, config.Provider, config.Model, keyPreview)
				fmt.Print("Press Enter to continue...")
				bufio.NewReader(os.Stdin).ReadString('\n')
			}

		case "exec":
			var execAction string
			toggleLabel := "Enable Auto-Execution"
			if config.AutoExecute {
				toggleLabel = "Disable Auto-Execution"
			}

			err := huh.NewForm(
				huh.NewGroup(
					huh.NewSelect[string]().
						Title("Execution Settings").
						Options(
							huh.NewOption(toggleLabel, "toggle"),
							huh.NewOption("Back", "back"),
						).
						Value(&execAction),
				),
			).Run()

			if err == nil && execAction == "toggle" {
				config.AutoExecute = !config.AutoExecute
				saveConfig()
				status := "OFF"
				if config.AutoExecute {
					status = "ON"
				}
				fmt.Printf("\n%sAuto-execution is now %s.%s\n", colorGreen, status, colorReset)
				time.Sleep(1 * time.Second)
			}

		case "context":
			var contextActions []string
			if config.EnableHistory { contextActions = append(contextActions, "history") }
			if config.EnableSessionMemory { contextActions = append(contextActions, "session") }
			if config.EnablePersistentMemory { contextActions = append(contextActions, "persistent") }

			err := huh.NewForm(
				huh.NewGroup(
					huh.NewMultiSelect[string]().
						Title("Context & Memory Settings").
						Description("Select which AI memory features to enable (Space to toggle):").
						Options(
							huh.NewOption("Command History Context", "history"),
							huh.NewOption("AI Session Memory", "session"),
							huh.NewOption("AI Persistent Memory", "persistent"),
						).
						Value(&contextActions),
				),
			).Run()

			if err == nil {
				config.EnableHistory = contains(contextActions, "history")
				config.EnableSessionMemory = contains(contextActions, "session")
				config.EnablePersistentMemory = contains(contextActions, "persistent")
				saveConfig()
				fmt.Printf("\n%sContext settings updated.%s\n", colorGreen, colorReset)
				time.Sleep(1 * time.Second)
			}

		case "network":
			proxyURL := config.ProxyURL
			err := huh.NewForm(
				huh.NewGroup(
					huh.NewInput().
						Title("Proxy URL (HTTP/SOCKS5)").
						Description("Leave blank to disable proxy (e.g., socks5://127.0.0.1:1080)").
						Value(&proxyURL).
						Validate(func(s string) error {
							if s == "" {
								return nil
							}
							u, err := url.Parse(s)
							if err != nil {
								return err
							}
							if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" {
								return fmt.Errorf("must start with http://, https://, or socks5://")
							}
							return nil
						}),
				),
			).Run()

			if err == nil {
				config.ProxyURL = proxyURL
				saveConfig()
				fmt.Printf("\n%sNetwork settings updated.%s\n", colorGreen, colorReset)
				time.Sleep(1 * time.Second)
			}
		}
	}
}

func getHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	if config.ProxyURL != "" {
		if proxyURL, err := url.Parse(config.ProxyURL); err == nil {
			transport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
	}
}

func fetchAIRegistry() AIRegistry {
	registry := AIRegistry{
		Providers: []ProviderConfig{
			{ID: "google", Name: "Google", Models: []string{}},
			{ID: "openai", Name: "OpenAI", Models: []string{}},
			{ID: "anthropic", Name: "Anthropic", Models: []string{}},
			{ID: "groq", Name: "Groq", Models: []string{}},
			{ID: "openrouter", Name: "OpenRouter", Models: []string{}},
			{ID: "vertex", Name: "Vertex AI", Models: []string{}},
			{ID: "ollama", Name: "Ollama (Local)", Models: []string{}},
			{ID: "lmstudio", Name: "LM Studio (Local)", Models: []string{}},
		},
	}

	client := getHTTPClient(5 * time.Second)
	// Fetching real-time models from OpenRouter as a primary source for discovery
	resp, err := client.Get("https://openrouter.ai/api/v1/models")
	if err == nil && resp.StatusCode == 200 {
		defer resp.Body.Close()
		var orResp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&orResp); err == nil {
			dynamicModels := make(map[string][]string)
			var orModels []string
			for _, m := range orResp.Data {
				orModels = append(orModels, m.ID)
				parts := strings.SplitN(m.ID, "/", 2)
				if len(parts) == 2 {
					dynamicModels[parts[0]] = append(dynamicModels[parts[0]], parts[1])
				}
			}
			for i, prov := range registry.Providers {
				if prov.ID == "openrouter" {
					registry.Providers[i].Models = orModels
				} else if models, ok := dynamicModels[prov.ID]; ok && len(models) > 0 {
					registry.Providers[i].Models = models
				}
			}
		}
	}
	return registry
}

func fetchModelsForProvider(provider, apiKey string) []string {
	var apiURL string
	switch provider {
	case "groq":
		apiURL = "https://api.groq.com/openai/v1/models"
	case "openai":
		apiURL = "https://api.openai.com/v1/models"
	case "google":
		apiURL = "https://generativelanguage.googleapis.com/v1beta/models?key=" + apiKey
	case "ollama":
		apiURL = "http://localhost:11434/v1/models"
	case "lmstudio":
		apiURL = "http://localhost:1234/v1/models"
	default:
		return nil
	}

	client := getHTTPClient(5 * time.Second)
	req, _ := http.NewRequest("GET", apiURL, nil)
	if provider != "google" && apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return nil
	}
	defer resp.Body.Close()

	var models []string
	if provider == "google" {
		var gResp struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&gResp); err == nil {
			for _, m := range gResp.Models {
				name := strings.TrimPrefix(m.Name, "models/")
				models = append(models, name)
			}
		}
	} else {
		var oResp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&oResp); err == nil {
			for _, m := range oResp.Data {
				models = append(models, m.ID)
			}
		}
	}
	return models
}

func handleModelConfig(ctx context.Context) {
	fmt.Printf("\n%sFetching latest AI providers and models...%s\n", colorCyan, colorReset)
	registry := fetchAIRegistry()

	var providerOptions []huh.Option[string]
	for _, prov := range registry.Providers {
		providerOptions = append(providerOptions, huh.NewOption(prov.Name, prov.ID))
	}

	p := config.Provider
	k := config.APIKey

	// Step 1: Provider and Key Selection
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("AI Provider").
				Options(providerOptions...).
				Value(&p),
			huh.NewInput().
				Title("API Key (leave blank for local providers)").
				EchoMode(huh.EchoModePassword).
				Value(&k),
		),
	).Run()

	if err != nil {
		fmt.Printf("\n%sConfiguration cancelled:%s %v\n", colorRed, colorReset, err)
		return
	}

	fmt.Printf("\n%sFetching latest models for selected provider...%s\n", colorCyan, colorReset)
	
	// Try fetching directly from provider first, then fallback to registry (OpenRouter)
	fetchedModels := fetchModelsForProvider(p, k)
	if len(fetchedModels) == 0 {
		for _, prov := range registry.Providers {
			if prov.ID == p {
				fetchedModels = prov.Models
				break
			}
		}
	}

	var modelOptions []huh.Option[string]
	for _, mod := range fetchedModels {
		modelOptions = append(modelOptions, huh.NewOption(mod, mod))
	}
	modelOptions = append(modelOptions, huh.NewOption("Custom (Type manually)", "custom"))

	m := config.Model
	if len(fetchedModels) > 0 && (m == "" || !contains(fetchedModels, m)) {
		m = fetchedModels[0]
	}

	// Step 2: Model Selection
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Model").
				Description("Select a model or type manually...").
				Height(10).
				Options(modelOptions...).
				Value(&m),
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

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// generateCommandFromAI uses the configured AI API to translate natural language into commands
func generateCommandFromAI(ctx context.Context, input string, done chan bool) (string, bool) {
	var stopSpinnerOnce sync.Once
	stopSpinner := func() {
		stopSpinnerOnce.Do(func() {
			done <- true
		})
	}
	defer stopSpinner() // Ensure spinner channel is always resolved

	if config.APIKey == "" && config.Provider != "ollama" && config.Provider != "lmstudio" {
		stopSpinner()
		fmt.Printf("\r%sError: API key is not configured. Please use '/setup' to set it.%s\n", colorRed, colorReset)
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
	case "ollama":
		baseURL = "http://localhost:11434/v1/chat/completions"
	case "lmstudio":
		baseURL = "http://localhost:1234/v1/chat/completions"
	default:
		// Use OpenRouter as the unified source to hit the latest API endpoints for any other provider
		baseURL = "https://openrouter.ai/api/v1/chat/completions"
		modelID = config.Provider + "/" + config.Model
	}

	targetOS := runtime.GOOS
	if runtime.GOOS == "darwin" {
		targetOS = "macOS"
	} else if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("powershell"); err == nil {
			targetOS = "Windows PowerShell"
		} else {
			targetOS = "Windows Command Prompt (cmd.exe)"
		}
	} else if runtime.GOOS == "linux" {
		targetOS = "Linux (bash/sh)"
	}

	cwd, _ := os.Getwd()

	var historyContext string
	if config.EnableHistory && len(sessionHistory) > 0 {
		historyContext = "\n\nRecent command history (for context):\n"
		for i, h := range sessionHistory {
			historyContext += fmt.Sprintf("[%d] User: %q -> Executed: %q\n", i+1, h.UserInput, h.Command)
		}
	}

	var persistentMemoryContext string
	if config.EnablePersistentMemory && config.AIMemory != "" {
		persistentMemoryContext = fmt.Sprintf("\n\nPersistent Memory (maintained by you):\n%s\n", config.AIMemory)
	}

	var sessionMemoryContext string
	if config.EnableSessionMemory && sessionAIMemory != "" {
		sessionMemoryContext = fmt.Sprintf("\n\nSession Memory (current task context):\n%s\n", sessionAIMemory)
	}

	instructions := `If the command is destructive or dangerous (e.g., delete, format, rmdir), prefix it EXACTLY with "UNSAFE: ".`
	if config.EnablePersistentMemory {
		instructions += "\nTo update your persistent memory, append a shell comment: \"# MEMORY: <new memory>\". This overwrites previous memory."
	}
	if config.EnableSessionMemory {
		instructions += "\nTo update your session memory, append a shell comment: \"# SESSION: <new session note>\". This overwrites previous session memory."
	}

	prompt := fmt.Sprintf(`You are a lightweight AI shell assistant for %s. 
Translate the user's natural language into a valid %s terminal command.
The current working directory is: %s%s%s%s
If the input is already a valid command, return it as is.
Return ONLY the raw command. Do not wrap it in quotes, markdown, or JSON.
%s
Otherwise, just output the command directly.

User input: %s`, targetOS, targetOS, cwd, historyContext, persistentMemoryContext, sessionMemoryContext, instructions, input)

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
	if config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+config.APIKey)
	}
	if baseURL == "https://openrouter.ai/api/v1/chat/completions" {
		req.Header.Set("HTTP-Referer", "https://github.com/tadB0x/IntelliShell")
		req.Header.Set("X-Title", "IntelliShell")
	}

	client := getHTTPClient(0)
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

	// Extract MEMORY and SESSION updates iteratively if the AI included them
	for {
		memIdx := strings.LastIndex(fullResponse, "# MEMORY:")
		sessIdx := strings.LastIndex(fullResponse, "# SESSION:")

		if memIdx == -1 && sessIdx == -1 {
			break
		}

		if memIdx > sessIdx {
			newMemory := strings.TrimSpace(fullResponse[memIdx+len("# MEMORY:"):])
			if newMemory != "" && config.EnablePersistentMemory {
				config.AIMemory = newMemory
				saveConfig()
			}
			fullResponse = strings.TrimSpace(fullResponse[:memIdx])
		} else {
			newSession := strings.TrimSpace(fullResponse[sessIdx+len("# SESSION:"):])
			if newSession != "" && config.EnableSessionMemory {
				sessionAIMemory = newSession
			}
			fullResponse = strings.TrimSpace(fullResponse[:sessIdx])
		}
	}

	return fullResponse, isSafe
}

func executeCommand(cmdStr string, forceUnsandboxed bool, rl *readline.Instance) {
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
	useSandbox := false
	if !forceUnsandboxed && isSandboxSupported() {
		useSandbox = true
	}

	if useSandbox {
		exe, _ := os.Executable()
		cmd = exec.Command(exe, "__sandbox_exec", cmdStr)
	} else if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("powershell"); err == nil {
			cmd = exec.Command("powershell", "-Command", cmdStr)
		} else {
			cmd = exec.Command("cmd", "/c", cmdStr)
		}
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
		if useSandbox && isSigSys(err) {
			fmt.Printf("\n%s🛡️  Seccomp Sandbox Triggered!%s\n", colorRed, colorReset)
			fmt.Printf("%sThe command attempted a restricted system call (e.g., file deletion).%s\n", colorYellow, colorReset)
			fmt.Printf("%sExecute anyway without restrictions? (y/n):%s ", colorYellow, colorReset)
			
			confirmLine, readErr := rl.ReadlineWithDefault("")
			if readErr != nil {
				return
			}
			if strings.ToLower(strings.TrimSpace(confirmLine)) == "y" {
				executeCommand(cmdStr, true, rl) // recursively re-run unsandboxed
			} else {
				fmt.Println("Execution cancelled.")
			}
			return
		}
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

// isNativeCommand uses heuristics to solidify the distinction between natural language prompts 
// and valid native system commands, bypassing the AI for immediate execution.
func isNativeCommand(input string) bool {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return false
	}

	cmd := parts[0]
	cmdLower := strings.ToLower(cmd)

	// 1. Built-in shell commands
	builtins := map[string]bool{
		"cd": true, "echo": true, "exit": true, "clear": true, "cls": true,
		"dir": true, "type": true, "del": true, "copy": true, "move": true,
		"ren": true, "md": true, "rd": true, "history": true, "alias": true,
		"export": true, "set": true, "source": true, "pwd": true, "pushd": true, "popd": true,
		"jobs": true, "fg": true, "bg": true, "kill": true, "unset": true,
	}
	if builtins[cmdLower] {
		return true
	}

	// 2. Direct path execution
	if strings.HasPrefix(cmd, "./") || strings.HasPrefix(cmd, ".\\") || filepath.IsAbs(cmd) || strings.HasPrefix(cmd, "/") {
		return true
	}

	// 3. Executables in PATH
	if _, err := exec.LookPath(cmd); err == nil {
		if len(parts) == 1 {
			return true // Exact command match without arguments (e.g., "ls")
		}

		// Known CLI tools with subcommands or specific use-cases that shouldn't be confused with natural language
		subcommands := map[string]bool{
			"git": true, "docker": true, "kubectl": true, "npm": true,
			"go": true, "cargo": true, "apt": true, "brew": true,
			"yarn": true, "make": true, "systemctl": true, "pip": true,
			"python": true, "python3": true, "node": true, "vim": true,
			"nano": true, "code": true, "grep": true, "awk": true, "sed": true,
			"cat": true, "tail": true, "head": true, "less": true,
			"tar": true, "unzip": true, "curl": true, "wget": true,
			"ssh": true, "scp": true, "rsync": true, "ping": true,
			"netstat": true, "ifconfig": true, "ip": true, "top": true,
			"htop": true, "ps": true, "df": true, "du": true,
			"free": true, "chmod": true, "chown": true, "rm": true,
			"mkdir": true, "touch": true, "mv": true, "cp": true,
			"sudo": true, "su": true, "bash": true, "sh": true, "zsh": true,
			"fish": true, "tmux": true, "screen": true, "nohup": true,
			"gcc": true, "g++": true, "clang": true, "clang++": true,
			"java": true, "javac": true, "rustc": true, "ruby": true,
			"perl": true, "lua": true,
		}
		if subcommands[cmdLower] {
			return true
		}

		// 4. Strong indicators of a native shell pipeline/redirection
		if strings.Contains(input, " |") || strings.Contains(input, " >") ||
			strings.Contains(input, " <") || strings.Contains(input, " &&") ||
			strings.Contains(input, " ||") || strings.Contains(input, " ;") {
			return true
		}

		// 5. Check if subsequent arguments look like explicit flags, paths, or assignments
		for _, arg := range parts[1:] {
			if (strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--")) && len(arg) > 1 {
				return true
			}
			if strings.HasPrefix(arg, "/") && len(arg) > 1 {
				return true
			}
			if strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, ".\\") || strings.HasPrefix(arg, "../") || strings.HasPrefix(arg, "..\\") {
				return true
			}
			if strings.Contains(arg, "=") {
				return true
			}
		}

		// If it's an executable but looks like natural language (e.g., "find all large files"),
		// we return false and let the AI translate it properly.
		return false
	}

	return false
}
