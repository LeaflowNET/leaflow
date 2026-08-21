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

	"github.com/LeaflowNET/leaflow/cmd/leaflow/internal/extension"
	"github.com/LeaflowNET/leaflow/internal/auth"
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
			creds, err = signInWithToken(cmd, ext)
		case useDevice:
			creds, err = signInWithDeviceCode(cmd, ext, noBrowser)
		default:
			creds, err = signInWithBrowser(cmd, ext, noBrowser)
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
//
// Pressing Enter switches to the device flow, and that is not a nicety. The
// redirect address is a loopback port on *this* machine. Over ssh the URL is
// printed on the server while the browser opens on the laptop, so the callback
// arrives at the laptop's own port where nothing is listening, and sign-in
// hangs until it times out with no indication of why. Someone in that position
// needs a way out that does not require knowing any of the above.
func signInWithBrowser(cmd *cobra.Command, ext *extension.Context, noBrowser bool) (*auth.Credentials, error) {
	out := cmd.ErrOrStderr()
	canPrompt := isInteractive(cmd)

	abort := make(chan struct{})

	if canPrompt {
		// Reads until a newline. The goroutine outlives a successful sign-in,
		// which is fine: nothing else reads stdin afterwards and the process is
		// about to exit.
		go func() {
			_, _ = bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
			close(abort)
		}()
	}

	creds, err := ext.Tokens.Login(cmd.Context(), func(target string) {
		if noBrowser || !canPrompt {
			fmt.Fprintf(out, "open this URL to sign in:\n\n  %s\n\n", target)
		} else {
			fmt.Fprintln(out, "opening your browser to sign in...")
			fmt.Fprintf(out, "if it does not open, use:\n\n  %s\n\n", target)
			auth.OpenBrowser(target)
		}

		if canPrompt {
			fmt.Fprintln(out, "That address is local to this machine, so it will not work")
			fmt.Fprintln(out, "if you opened the link somewhere else — over ssh, for instance.")
			fmt.Fprintln(out, "Press Enter for a code you can approve from any device.")
			fmt.Fprintln(out)
		}
	}, abort)

	if errors.Is(err, auth.ErrLoginAborted) {
		fmt.Fprintln(out, "Switching to device sign-in.")
		fmt.Fprintln(out)

		return signInWithDeviceCode(cmd, ext, noBrowser)
	}

	return creds, err
}

func signInWithDeviceCode(cmd *cobra.Command, ext *extension.Context, noBrowser bool) (*auth.Credentials, error) {
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
	fmt.Fprintf(out, "then open: %s\n", target)

	// No pause for a keypress here. This is reached either from a machine with
	// no browser, or from the browser flow having just consumed the Enter that
	// got us here — waiting for a second one would look like a hang.
	if !noBrowser && isInteractive(cmd) {
		auth.OpenBrowser(target)
	}

	fmt.Fprintln(out, "\nwaiting for confirmation...")

	return wait()
}

// signInWithToken reads a refresh token from stdin.
//
// Piped rather than passed as a flag: an argument is visible in `ps` and lands
// in shell history, and neither is where a credential belongs.
func signInWithToken(cmd *cobra.Command, ext *extension.Context) (*auth.Credentials, error) {
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

// isInteractive reports whether stdin is a terminal, which decides whether the
// CLI may wait for a keypress. In a pipeline nobody is there to press anything
// and blocking would hang the job.
//
// term.IsTerminal and not a ModeCharDevice check: /dev/null is a character
// device too, so `leaflow login --device < /dev/null` would look isInteractive
// and stop to wait for a key that never arrives.
func isInteractive(cmd *cobra.Command) bool {
	file, ok := cmd.InOrStdin().(*os.File)
	if !ok {
		return false
	}

	return term.IsTerminal(int(file.Fd()))
}
