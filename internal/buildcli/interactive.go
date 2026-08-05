package buildcli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

// InteractivePurpose identifies why a command needs a native terminal.
type InteractivePurpose string

const (
	InteractiveSteamAuthentication    InteractivePurpose = "steam-authentication"
	InteractiveDependencyInstallation InteractivePurpose = "dependency-installation"
)

// InteractiveCommand describes an external command that must own a real
// terminal. Arguments are kept separate so frontends do not need to construct
// a shell command line.
type InteractiveCommand struct {
	Purpose          InteractivePurpose `json:"purpose"`
	Executable       string             `json:"executable"`
	Arguments        []string           `json:"arguments"`
	WorkingDirectory string             `json:"workingDirectory,omitempty"`
	Environment      map[string]string  `json:"environment,omitempty"`
}

// InteractiveCommandRunner runs commands whose private prompts require a real
// terminal, such as SteamCMD password and Steam Guard authentication.
type InteractiveCommandRunner interface {
	RunInteractive(context.Context, InteractiveCommand) error
}

// InteractiveCommandRunnerFunc adapts a function to InteractiveCommandRunner.
type InteractiveCommandRunnerFunc func(context.Context, InteractiveCommand) error

// RunInteractive implements InteractiveCommandRunner.
func (function InteractiveCommandRunnerFunc) RunInteractive(ctx context.Context, command InteractiveCommand) error {
	return function(ctx, command)
}

// GeneralsX @feature Codex 05/08/2026 Move only prompt-capable commands into a native terminal supplied by a GUI.
func (app application) runInteractive(ctx context.Context, spec command, purpose InteractivePurpose) error {
	if app.interactiveRunner == nil || app.cfg.dryRun {
		return app.runner.run(ctx, spec)
	}
	if spec.name == "" {
		return errors.New("interactive command name is empty")
	}
	fmt.Fprintf(app.runner.stdout, "> %s\n", renderCommand(spec))
	interactive := InteractiveCommand{
		Purpose:          purpose,
		Executable:       spec.name,
		Arguments:        append([]string(nil), spec.args...),
		WorkingDirectory: spec.dir,
	}
	if len(spec.env) != 0 {
		interactive.Environment = make(map[string]string, len(spec.env))
		for key, value := range spec.env {
			interactive.Environment[key] = value
		}
	}
	if err := app.interactiveRunner.RunInteractive(ctx, interactive); err != nil {
		return fmt.Errorf("run %s in terminal: %w", filepath.Base(spec.name), err)
	}
	return nil
}

// RunOptions configures optional, UI-neutral observers and interactive command
// handling. Nil fields retain the terminal command's established behavior.
type RunOptions struct {
	Reporter          Reporter
	InteractiveRunner InteractiveCommandRunner
}
