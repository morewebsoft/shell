# IntelliShell 🧠💻

IntelliShell is a lightweight, cross-platform AI shell assistant that allows you to control your terminal using natural language. It seamlessly translates your plain-text instructions into valid, native terminal commands for Windows, macOS, and Linux.

## ✨ Features

- **Natural Language to Command**: Type what you want to do in plain English, and let the AI translate it into the appropriate command for your OS.
- **Multi-Provider Support**: Supports multiple AI providers natively, including Google (Gemini), OpenAI, Anthropic, Vertex AI, and dynamically fetches models via OpenRouter.
- **Safety First**: Automatically detects potentially destructive commands (e.g., delete, format) and prompts for confirmation before execution.
- **Blazing Fast Presets**: Common operations (like listing files or checking the current directory) bypass the AI via pre-compiled Regex aliases for instantaneous execution.
- **Interactive Configuration**: Features a beautiful built-in terminal UI (powered by `charmbracelet/huh`) to configure your preferred AI provider, model, and API keys on the fly.
- **Native Execution**: Runs commands directly in your native shell environment, preserving interactivity, standard input/output streams, and internal directory states.

## 🚀 Getting Started

### Prerequisites
- Go (stable version recommended)

### Installation
Clone the repository and build the project:

```bash
git clone https://github.com/tadB0x/IntelliShell.git
cd IntelliShell
go build -o intellishell ./cmd/shell
```

### Usage
Run the executable to enter the interactive AI shell:

```bash
./intellishell
```

Upon launching, IntelliShell will default to Google's `gemini-1.5-flash` model, looking for the `GEMINI_API_KEY` environment variable. You can easily reconfigure this in the shell.

## 🛠️ Built-in Commands

- **`<natural language>`**: Describe your task (e.g., `"find all python files modified today"`, `"undo my last git commit"`).
- **`<native command>`**: Standard terminal commands work as usual (e.g., `ls`, `cd`, `git status`).
- **`/model`**: Opens the interactive AI provider and model configuration setup.
- **`/settings`**: Opens the preferences menu (Auto-execution, Modes, etc.).
- **`exit`**: Quits IntelliShell.

## 🛡️ Safety Mechanism

IntelliShell instructs the LLMs to safely flag destructive or dangerous commands. If an action is flagged, execution halts, and you will see a warning prompt:

`⚠️  Command might be unsafe. Execute? (y/n):`

## 🤝 Contributing

Contributions are highly encouraged! 
Want to speed up the shell? You can add community contributions for common commands to bypass the AI by adding logical actions to the `commandFamilies` array in `presets/presets.go`.

## 🚢 Releases

This project uses GoReleaser via GitHub Actions. Creating a new tag and pushing it will automatically build and publish release binaries for all supported platforms.

## 📝 License

This project is open-source. Please check the repository for further licensing details.