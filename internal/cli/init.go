package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/tickets"
	"github.com/spf13/cobra"
)

const (
	starterFactory  = "factory"
	runnerTokenFile = config.StateDir + "/runner.token"
	localRunnerName = "local"
	gitignoreFile   = ".gitignore"
	gitignoreEntry  = "/" + config.StateDir + "/"
)

var initialFiles = []string{
	"config.toml",
	"runner.toml",
}

func newInitCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "init [directory]",
		Short: "Create the default Machinist configuration in a directory, the current one by default",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			directory := "."
			if len(args) == 1 {
				directory = args[0]
			}
			return initializeMachinist(command.Context(), directory, options.stdout)
		},
	}
}

// initializeMachinist writes the configuration and factory, which belong in
// git, and the state directory, which does not.
func initializeMachinist(ctx context.Context, directory string, output io.Writer) error {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", directory, err)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", directory, err)
	}
	if err := ensureDirectory(filepath.Join(directory, config.StateDir)); err != nil {
		return err
	}
	if err := ignoreStateDirectory(directory, output); err != nil {
		return err
	}

	for _, name := range initialFiles {
		body, err := machinistexamples.Files.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read default %s: %w", name, err)
		}
		if err := installFile(directory, name, body, output); err != nil {
			return err
		}
	}
	if err := installFactory(directory, output); err != nil {
		return err
	}
	if err := installRunnerToken(ctx, directory, output); err != nil {
		return err
	}
	fmt.Fprintf(output, "Machinist configuration is ready in %s\n", directory)
	fmt.Fprintf(output, "Commit config.toml, runner.toml and factory/. %s/ holds the database and tokens and stays out of git.\n", config.StateDir)
	fmt.Fprintln(output, "Set github.handle and repositories in config.toml, and env in runner.toml.")
	return nil
}

// ignoreStateDirectory adds the state directory to .gitignore, so the database
// and tokens are never committed with the configuration.
func ignoreStateDirectory(directory string, output io.Writer) error {
	path := filepath.Join(directory, gitignoreFile)
	body, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", gitignoreFile, err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		switch strings.TrimSpace(line) {
		case gitignoreEntry, strings.TrimPrefix(gitignoreEntry, "/"), strings.Trim(gitignoreEntry, "/"), "/" + config.StateDir:
			return nil
		}
	}
	if len(body) > 0 && !strings.HasSuffix(string(body), "\n") {
		body = append(body, '\n')
	}
	body = append(body, gitignoreEntry+"\n"...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", gitignoreFile, err)
	}
	fmt.Fprintf(output, "added %s to %s\n", gitignoreEntry, gitignoreFile)
	return nil
}

// installFactory copies the starter factory without replacing the team's files.
func installFactory(directory string, output io.Writer) error {
	return fs.WalkDir(machinistexamples.Files, starterFactory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return ensureDirectory(filepath.Join(directory, filepath.FromSlash(path)))
		}
		body, err := machinistexamples.Files.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read default %s: %w", path, err)
		}
		return installFile(directory, path, body, output)
	})
}

// installRunnerToken makes the token the local runner starts with. A token named
// local whose file was lost can never be read again, so it is replaced.
func installRunnerToken(ctx context.Context, directory string, output io.Writer) error {
	path := filepath.Join(directory, runnerTokenFile)
	if _, err := os.Lstat(path); err == nil {
		fmt.Fprintf(output, "kept %s\n", runnerTokenFile)
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect existing %s: %w", runnerTokenFile, err)
	}
	machinistConfig, err := config.LoadConfig(filepath.Join(directory, "config.toml"))
	if err != nil {
		return err
	}
	store, err := tickets.Open(machinistConfig.Server.Database)
	if err != nil {
		return err
	}
	defer store.Close()
	token, err := store.CreateToken(ctx, localRunnerName)
	if errors.Is(err, tickets.ErrTokenExists) {
		if err = store.RevokeToken(ctx, localRunnerName); err == nil {
			token, err = store.CreateToken(ctx, localRunnerName)
		}
	}
	if err != nil {
		return fmt.Errorf("create runner token: %w", err)
	}
	return installFile(directory, runnerTokenFile, []byte(token+"\n"), output)
}

func installFile(root, name string, body []byte, output io.Writer) error {
	path := filepath.Join(root, filepath.FromSlash(name))
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return fmt.Errorf("inspect existing %s: %w", name, statErr)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s already exists and is not a regular file", name)
		}
		fmt.Fprintf(output, "kept %s\n", name)
		return nil
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	written, writeErr := file.Write(body)
	if writeErr == nil && written != len(body) {
		writeErr = io.ErrShortWrite
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("write %s: %w", name, writeErr)
	}
	fmt.Fprintf(output, "created %s\n", name)
	return nil
}

func ensureDirectory(path string) error {
	err := os.Mkdir(path, 0o700)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create directory %s: %w", path, err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		return fmt.Errorf("inspect existing directory %s: %w", path, statErr)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s already exists and is not a directory", path)
	}
	return nil
}
