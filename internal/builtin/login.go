package builtin

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/LeaflowNET/leaflow/internal/auth"
	"github.com/LeaflowNET/leaflow/internal/extension"
)

var ErrNoTokenOnStdin = errors.New("no token on stdin")

func newLoginCommand(ext *extension.Context) *cobra.Command {
	var (
		useDevice bool
		noBrowser bool
		withToken bool
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "sign in to the platform",
		Long: `Sign in through the browser using authorization code with PKCE.

This CLI is a public client: it ships to every machine and holds no secret.
PKCE supplies one per attempt instead — the verifier never leaves this process
and only its hash is sent — so an intercepted authorization code is useless.
The redirect is a loopback listener on 127.0.0.1, on a port the OS picks.

Where a loopback redirect cannot arrive — over ssh, in a container, on a
headless host — use --device and approve the code from another machine.

For CI, pipe a refresh token in:

    echo "$LEAFLOW_REFRESH_TOKEN" | leaflow login --with-token

or set LEAFLOW_TOKEN to a project token and do not sign in at all.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&useDevice, "device", false,
		"sign in with a device code, for machines with no browser")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false,
		"print the URL instead of opening a browser")
	cmd.Flags().BoolVar(&withToken, "with-token", false,
		"read a refresh token from stdin instead of signing in interactively")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		var (
			creds *auth.Credentials
			err   error
		)

		switch {
		case withToken:
			creds, err = tokenLogin(cmd, ext)
		case useDevice:
			creds, err = deviceLogin(cmd, ext, noBrowser)
		default:
			creds, err = browserLogin(cmd, ext, noBrowser)
		}

		if err != nil {
			return err
		}

		cfg := ext.Config
		cfg.EditContext("").Account = creds.Account

		if err := cfg.Save(); err != nil {
			return err
		}

		result := map[string]any{
			"logged_in": true,
			"account":   creds.Account,
			"context":   cfg.CurrentName(),
		}

		if project := cfg.Context().Project; project == "" {
			result["next"] = "leaflow project use <project>"
		} else {
			result["project"] = project
		}

		return ext.Printer.Print(result)
	}

	return cmd
}

// Progress goes to stderr so a scripted `leaflow login -o json` keeps stdout to
// the result alone.
func browserLogin(cmd *cobra.Command, ext *extension.Context, noBrowser bool) (*auth.Credentials, error) {
	out := cmd.ErrOrStderr()

	return ext.Tokens.Login(cmd.Context(), func(target string) {
		if noBrowser || !interactive(cmd) {
			fmt.Fprintf(out, "open this URL to sign in:\n\n  %s\n\n", target)

			return
		}

		fmt.Fprintln(out, "opening your browser to sign in...")
		fmt.Fprintf(out, "if it does not open, use:\n\n  %s\n\n", target)
		auth.OpenBrowser(target)
	})
}

func deviceLogin(cmd *cobra.Command, ext *extension.Context, noBrowser bool) (*auth.Credentials, error) {
	out := cmd.ErrOrStderr()

	code, wait, err := ext.Tokens.LoginWithDeviceCode(cmd.Context())
	if err != nil {
		return nil, err
	}

	target := code.VerificationURI
	if target == "" {
		target = code.CompleteURI
	}

	fmt.Fprintf(out, "first copy your one-time code: %s\n", code.UserCode)

	// The code is shown before the browser opens, and the pause is what makes
	// that useful: a browser stealing focus mid-sentence is how people end up on
	// the verification page without having read the code.
	if interactive(cmd) && !noBrowser {
		fmt.Fprint(out, "press Enter to open the browser, or Ctrl-C to quit... ")
		_, _ = bufio.NewReader(cmd.InOrStdin()).ReadString('\n')

		if code.CompleteURI != "" {
			// This form carries the code, so there is nothing to paste.
			auth.OpenBrowser(code.CompleteURI)
		} else {
			auth.OpenBrowser(target)
		}
	} else {
		fmt.Fprintf(out, "then open: %s\n", target)
	}

	fmt.Fprintln(out, "\nwaiting for confirmation...")

	return wait()
}

// tokenLogin reads a refresh token from stdin.
//
// Piped rather than passed as a flag: an argument is visible in `ps` and lands
// in shell history, and neither is where a credential belongs.
func tokenLogin(cmd *cobra.Command, ext *extension.Context) (*auth.Credentials, error) {
	data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64*1024))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoTokenOnStdin, err)
	}

	token := strings.TrimSpace(string(data))
	if token == "" {
		return nil, ErrNoTokenOnStdin
	}

	return ext.Tokens.LoginWithRefreshToken(cmd.Context(), token)
}

// interactive reports whether stdin is a terminal, which decides whether the
// CLI may wait for a keypress. In a pipeline nobody is there to press anything
// and blocking would hang the job.
//
// term.IsTerminal and not a ModeCharDevice check: /dev/null is a character
// device too, so `leaflow login --device < /dev/null` would look interactive
// and stop to wait for a key that never arrives.
func interactive(cmd *cobra.Command) bool {
	file, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return false
	}

	return term.IsTerminal(int(file.Fd()))
}
