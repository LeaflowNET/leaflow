package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// newCompletionInstallCommand writes a completion script to the place the
// shell already reads, instead of printing one and telling the reader where to
// put it.
//
// cobra's own `completion` command prints to stdout, which is right for a
// package manager and wrong for a person: the instructions differ per shell,
// per platform, and per whether the shell was installed by Homebrew. Writing
// the file is the part nobody wants to look up.
func newCompletionInstallCommand(root *cobra.Command) *cobra.Command {
	var shellName string

	cmd := &cobra.Command{
		Use:   "install-completion",
		Short: "install shell completion for the current shell",
		Long: `Write a completion script to the directory this shell loads from.

The shell is detected from $SHELL unless --shell says otherwise. Print the
script instead with: leaflow completion <shell>`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().StringVar(&shellName, "shell", "", "bash, zsh or fish; default from $SHELL")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		shell := shellName
		if shell == "" {
			shell = detectShell()
		}

		path, err := findCompletionPath(shell)
		if err != nil {
			return err
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("%w: %v", ErrCompletionFailed, err)
		}

		file, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrCompletionFailed, err)
		}
		defer file.Close()

		switch shell {
		case "bash":
			err = root.GenBashCompletionV2(file, true)
		case "zsh":
			err = root.GenZshCompletion(file)
		case "fish":
			err = root.GenFishCompletion(file, true)
		default:
			return fmt.Errorf("%w: %s", ErrUnsupportedShell, shell)
		}

		if err != nil {
			return fmt.Errorf("%w: %v", ErrCompletionFailed, err)
		}

		out := cmd.ErrOrStderr()
		fmt.Fprintf(out, "Wrote %s\n", path)

		// zsh only reads a completion directory that is on $fpath, and adding it
		// is the step people get wrong. bash-completion and fish scan theirs.
		if shell == "zsh" {
			fmt.Fprintf(out, "\nIf completion does not work, add this to ~/.zshrc before compinit:\n")
			fmt.Fprintf(out, "  fpath=(%s $fpath)\n", filepath.Dir(path))
		}

		fmt.Fprintln(out, "\nStart a new shell to use it.")

		return nil
	}

	return cmd
}

func detectShell() string {
	shell := filepath.Base(os.Getenv("SHELL"))

	// Login shells appear as -zsh.
	return strings.TrimPrefix(shell, "-")
}

// findCompletionPath is where each shell looks without further configuration,
// under the user's own directories so that nothing needs sudo.
func findCompletionPath(shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrCompletionFailed, err)
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}

	switch shell {
	case "bash":
		return filepath.Join(dataHome, "bash-completion", "completions", "leaflow"), nil
	case "zsh":
		return filepath.Join(home, ".zsh", "completions", "_leaflow"), nil
	case "fish":
		configHome := os.Getenv("XDG_CONFIG_HOME")
		if configHome == "" {
			configHome = filepath.Join(home, ".config")
		}

		return filepath.Join(configHome, "fish", "completions", "leaflow.fish"), nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedShell, shell)
	}
}
