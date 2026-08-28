package cli

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	machinistexamples "github.com/owainlewis/machinist/examples"
	"github.com/spf13/cobra"
)

var initialFiles = []string{
	"config.toml",
	"worker.toml",
	"agents/foreman.md",
	"agents/audit.md",
	"agents/shepherd.md",
	"agents/pr-risk-reviewer.md",
}

func newInitCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the default Machinist configuration",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return initializeMachinist(options.stdout)
		},
	}
}

func initializeMachinist(output io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find user home directory: %w", err)
	}
	directory := filepath.Join(home, ".machinist")
	for _, path := range []string{directory, filepath.Join(directory, "agents"), filepath.Join(directory, "server")} {
		if err := ensureDirectory(path); err != nil {
			return err
		}
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

	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return fmt.Errorf("generate worker token: %w", err)
	}
	if err := installFile(directory, "server/worker.token", []byte(hex.EncodeToString(token)+"\n"), output); err != nil {
		return err
	}

	fmt.Fprintf(output, "Machinist configuration is ready in %s\n", directory)
	fmt.Fprintln(output, "Add repositories to worker.toml before starting a managed worker.")
	return nil
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
