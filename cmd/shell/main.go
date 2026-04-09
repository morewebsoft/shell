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

	"github.com/PuerkitoBio/goquery"
	"github.com/TadB0x/IntelliShell/presets"

	"github.com/chzyer/readline"
	"github.com/charmbracelet/huh"
)

type AppConfig struct {
	Provider     string
	Model        string
	APIKey       string
	AutoExecute  bool
	HistoryLimit int // New: Max number of history items to keep for context
}

type HistoryEntry struct {
	Timestamp string `json:"timestamp"`
	Input     string `json:"input"`
	Command   string `json:"command"`
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
	config = AppConfig{
		Provider:     "google",
		Model:        "gemini-1.5-flash",
		APIKey:       os.Getenv("GEMINI_API_KEY"),
		HistoryLimit: 10, // Default to last 10 items
	}
	history []HistoryEntry

	colorCyan   = "\033[36m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorPurple = "\033[35m"
	colorGrey   = "\033[90m"
	colorReset  = "\033[0m"
	clearLine   = "\r\033[K"
)

func getHistoryPath() string {
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".intellishell")
	_ = os.MkdirAll(configDir, 0755)
	return filepath.Join(configDir, "history.json")
}

func loadHistory() {
	path := getHistoryPath()
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &history)
	}
}

func saveHistory(entry HistoryEntry) {
	history = append(history, entry)
	// Keep only the last N items
	if len(history) > config.HistoryLimit {
		history = history[len(history)-config.HistoryLimit:]
	}
	path := getHistoryPath()
	data, _ := json.MarshalIndent(history, "", "  ")
	_ = os.WriteFile(path, data, 0644)
}

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

	cmds := []string{"/settings", "/model", "/version", "/help"}
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
		suggestion := strings.Join(matches, "  ")
		// Save cursor position, print suggestion, clear leftovers, restore cursor
		ghost := fmt.Sprintf("\033[s      %s%s%s\033[K\033[u", colorGrey, suggestion, colorReset)
		
		res := make([]rune, len(line))
		copy(res, line)
		res = append(res, []rune(ghost)...)
		return res
	}

	return line
}

func main() {
	loadConfig()
	loadHistory()
	ctx := context.Background()

	// Configure autocomplete for / commands
	completer := readline.NewPrefixCompleter(
		readline.PcItem("/settings"),
		readline.PcItem("/model"),
		readline.PcItem("/version"),
		readline.PcItem("/help"),
		readline.PcItem("exit"),
	)

	rl, err := readline.NewEx(&readline.Config{
		Prompt:          fmt.Sprintf("%s[AI] %s>%s ", colorCyan, "[PWD]", colorReset),
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

	fmt.Println("Welcome to IntelliShell. Type natural language, native commands, '/model' for AI setup, '/settings' for preferences, or 'exit' to quit.")

	for {
		cwd, _ := os.Getwd()
		rl.SetPrompt(fmt.Sprintf("%s[AI] %s>%s ", colorCyan, cwd, colorReset))

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

		// Handle the version command
		if strings.HasPrefix(input, "/version") {
			fmt.Printf("%sIntelliShell version 0.1%s\n", colorCyan, colorReset)
			continue
		}

		// Check for a preset command first for performance and to bypass AI
		if command, found := presets.CheckForPreset(input); found {
			fmt.Printf("%s-> %s%s\n", colorGreen, command, colorReset) // Show the resolved command
			// Presets are considered safe and are executed directly
			executeCommand(ctx, command, input)
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

		// Save to persistent history
		saveHistory(HistoryEntry{
			Timestamp: time.Now().Format(time.RFC3339),
			Input:     input,
			Command:   command,
		})

		// 2. Safety Verification
		if !isSafe || !config.AutoExecute {
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
		executeCommand(ctx, command, input)
	}
}

func handleSettings() {
	for {
		// Prepare status indicators
		apiStatus := "❌ Not Configured"
		if config.APIKey != "" || config.Provider == "ollama" || config.Provider == "lmstudio" {
			apiStatus = fmt.Sprintf("✅ Configured (%s)", config.Provider)
		}

		autoExecStatus := "🔴 OFF (Always Ask)"
		if config.AutoExecute {
			autoExecStatus = "🟢 ON (Ask only on unsafe)"
		}

		var category string
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("⚙️ IntelliShell Settings").
					Description("Choose a category to modify:").
					Options(
						huh.NewOption("🤖 AI Configuration (Current: "+apiStatus+")", "ai"),
						huh.NewOption("⚡ Execution Settings (Current: "+autoExecStatus+")", "exec"),
						huh.NewOption("🔙 Back to Shell", "exit"),
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
						Title("🤖 AI Configuration").
						Options(
							huh.NewOption("🔄 Update Model / API Key", "update"),
							huh.NewOption("👀 Show Current Config", "show"),
							huh.NewOption("🔙 Back", "back"),
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
						Title("⚡ Execution Settings").
						Options(
							huh.NewOption(toggleLabel, "toggle"),
							huh.NewOption("🔙 Back", "back"),
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
		}
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

	client := &http.Client{Timeout: 5 * time.Second}
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

	client := &http.Client{Timeout: 5 * time.Second}
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
	case "ollama":
		baseURL = "http://localhost:11434/v1/chat/completions"
	case "lmstudio":
		baseURL = "http://localhost:1234/v1/chat/completions"
	default:
		// Use OpenRouter as the unified source to hit the latest API endpoints for any other provider
		baseURL = "https://openrouter.ai/api/v1/chat/completions"
		modelID = config.Provider + "/" + config.Model
	}

	cwd, _ := os.Getwd()
	var historyContext string
	if len(history) > 0 {
		historyContext = "\nRecent history:\n"
		for _, h := range history {
			historyContext += fmt.Sprintf("- User: %s -> Cmd: %s\n", h.Input, h.Command)
		}
	}

	prompt := fmt.Sprintf(`You are a lightweight AI shell assistant for %s.%s
Translate the user's natural language into a valid %s terminal command.
The current working directory is: %s
If the input is already a valid command, return it as is.
Return ONLY the raw command. Do not wrap it in quotes, markdown, or JSON.
If the command is destructive or dangerous (e.g., delete, format, rmdir), prefix it EXACTLY with "UNSAFE: ".
Otherwise, just output the command directly.

User input: %s`, runtime.GOOS, historyContext, runtime.GOOS, cwd, input)

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

func searchForSolution(ctx context.Context, originalInput, failedCommand, errorMsg string) (string, bool) {
	// 1. LOCAL FALLBACK: If it's a package error on Linux, try apt-cache search
	if runtime.GOOS == "linux" && (strings.Contains(errorMsg, "Unable to locate package") || strings.Contains(errorMsg, "not found")) {
		fmt.Printf("\n%s🔍 Local search: Looking for package in apt-cache...%s\n", colorCyan, colorReset)
		// Try to extract package name from the original input or error
		words := strings.Fields(originalInput)
		for _, word := range words {
			if word == "install" || word == "get" { continue }
			out, err := exec.Command("apt-cache", "search", word).Output()
			if err == nil && len(out) > 0 {
				lines := strings.Split(string(out), "\n")
				if len(lines) > 0 {
					parts := strings.Split(lines[0], " - ")
					if len(parts) > 0 {
						pkgName := strings.TrimSpace(parts[0])
						fmt.Printf("%s💡 Found matching package: %s%s\n", colorGreen, pkgName, colorReset)
						return "sudo apt install " + pkgName, true
					}
				}
			}
		}
	}

	fmt.Printf("\n%s🔍 Command failed. Searching for a solution...%s\n", colorCyan, colorReset)

	// Simple DuckDuckGo search query
	query := fmt.Sprintf("how to %s on %s fix error %s", originalInput, runtime.GOOS, errorMsg)
	searchURL := "https://duckduckgo.com/html/?q=" + url.QueryEscape(query)

	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", false
	}

	var searchResults []string
	doc.Find(".result__snippet").Each(func(i int, s *goquery.Selection) {
		if i < 3 { // Get top 3 snippets
			searchResults = append(searchResults, s.Text())
		}
	})

	if len(searchResults) == 0 {
		return "", false
	}

	contextInfo := strings.Join(searchResults, "\n")
	prompt := fmt.Sprintf(`The previous command failed.
Original Intent: %s
Failed Command: %s
Error Message: %s

Web Search Context:
%s

Based on this, provide a corrected, working terminal command for %s.
Return ONLY the raw command. Do not wrap it in quotes, markdown, or JSON.
If it's dangerous, prefix with "UNSAFE: ".`, originalInput, failedCommand, errorMsg, contextInfo, runtime.GOOS)

	// Call AI with this new prompt
	correctedCmd, isSafe := callAIForCorrection(ctx, prompt)
	return correctedCmd, isSafe
}

func callAIForCorrection(ctx context.Context, prompt string) (string, bool) {
	var baseURL string
	modelID := config.Model

	switch config.Provider {
	case "google":
		baseURL = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
	case "openai":
		baseURL = "https://api.openai.com/v1/chat/completions"
	case "groq":
		baseURL = "https://api.groq.com/openai/v1/chat/completions"
	default:
		baseURL = "https://openrouter.ai/api/v1/chat/completions"
		if !strings.Contains(modelID, "/") && config.Provider != "" {
			modelID = config.Provider + "/" + config.Model
		}
	}

	reqBody := map[string]interface{}{
		"model": modelID,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.1,
	}

	jsonData, _ := json.Marshal(reqBody)
	req, _ := http.NewRequestWithContext(ctx, "POST", baseURL, bytes.NewReader(jsonData))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.APIKey)

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return "", true
	}
	defer resp.Body.Close()

	var aiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&aiResp); err != nil || len(aiResp.Choices) == 0 {
		return "", true
	}

	content := strings.TrimSpace(aiResp.Choices[0].Message.Content)
	isSafe := true
	if strings.HasPrefix(content, "UNSAFE:") {
		isSafe = false
		content = strings.TrimSpace(strings.TrimPrefix(content, "UNSAFE:"))
	}

	return content, isSafe
}

func executeCommand(ctx context.Context, cmdStr, originalInput string) {
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
	var stderr bytes.Buffer
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	cmd.Stdin = os.Stdin

	err := cmd.Run()
	if err != nil {
		errorMsg := stderr.String()
		if errorMsg == "" {
			errorMsg = err.Error()
		}

		// FALLBACK SYSTEM: Search for a solution if command failed
		correctedCmd, isSafe := searchForSolution(ctx, originalInput, cmdStr, errorMsg)
		if correctedCmd != "" && correctedCmd != cmdStr {
			fmt.Printf("\n%s💡 Suggested Fix: %s%s\n", colorGreen, correctedCmd, colorReset)
			if !isSafe {
				fmt.Printf("%s⚠️  Corrected command might be unsafe. Execute? (y/n):%s ", colorYellow, colorReset)
			} else {
				fmt.Printf("Execute this fix? (y/n): ")
			}

			// We need a way to read from stdin properly here.
			// Since we're in the middle of potential command chains, we'll use a simple reader.
			reader := bufio.NewReader(os.Stdin)
			confirm, _ := reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(confirm)) == "y" {
				executeCommand(ctx, correctedCmd, originalInput) // Recurse with new command
			}
		} else {
			fmt.Printf("%sCommand failed:%s %v\n", colorRed, colorReset, err)
		}
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
