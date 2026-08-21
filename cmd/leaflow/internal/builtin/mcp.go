package builtin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/LeaflowNET/leaflow/cmd/leaflow/internal/extension"
	"github.com/LeaflowNET/leaflow/pkg/leaflow"
	"github.com/LeaflowNET/leaflow/pkg/mcp"
)

var ErrAddressNotLocal = errors.New("refusing to listen beyond this machine")

// newMCPCommand serves the contracts to an assistant.
//
// The same binary, the same contracts, the same credentials — only the caller
// changes. Nothing about the platform is restated here: a tool exists because
// an operation exists, and it accepts what the contract says it accepts.
func newMCPCommand(ext *extension.Context, version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "serve the platform over the Model Context Protocol",
		Long: `Serve this CLI's operations as MCP tools.

An assistant that speaks MCP can then work the platform directly, through the
contracts this binary already carries and the credentials this machine already
holds. There is no separate service to deploy and nothing to keep in sync: a
tool is an operation, named by the same identifier the command line uses.`,
	}

	cmd.AddCommand(newMCPServeCommand(ext, version))

	return cmd
}

func newMCPServeCommand(ext *extension.Context, version string) *cobra.Command {
	var (
		tools       string
		services    []string
		readOnly    bool
		address     string
		allowRemote bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "run the MCP server",
		Long: `Run an MCP server.

On stdin and stdout by default, which is how a local client starts one:

  {
    "mcpServers": {
      "leaflow": { "command": "leaflow", "args": ["mcp", "serve"] }
    }
  }

Two hundred operations ship with this binary, and a client is sent every tool
definition on every turn. So by default four tools are exposed — services,
operations, operation-schema, call-operation — and the client walks the
contracts through them. Pass --tools operations to get one tool per operation
instead, which clients handle better but which is only sensible once --service
has narrowed it to a service or two.

Replies come back as XML: a closing tag says which thing ended, which is what
survives being read out of the middle of a long reply.

The server acts as whoever ran "leaflow login", in whichever project is
selected. It cannot sign in on its own. --read-only is how to hand an assistant
the platform without handing it the ability to change anything.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	flags := cmd.Flags()
	flags.StringVar(&tools, "tools", string(mcp.ModeMeta),
		"how operations are exposed: "+strings.Join(mcp.Modes, ", "))
	flags.StringSliceVar(&services, "service", nil,
		"limit to these services; repeatable. Default is every contract")
	flags.BoolVar(&readOnly, "read-only", false,
		"expose only operations that read, dropping every write before a client sees it")
	flags.StringVar(&address, "http", "",
		"serve over HTTP on this address instead of stdin/stdout, e.g. 8080 or 127.0.0.1:8080")
	flags.BoolVar(&allowRemote, "allow-remote", false,
		"allow --http to listen beyond this machine. The server has no authentication of its own "+
			"and carries this machine's credentials; put it behind something that does")

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		// The library holds the contracts and the credentials; this command only
		// decides which of them to expose and where to listen. The token manager
		// goes in as the credential source rather than a token, because it is the
		// one that can renew and exchange.
		client, err := leaflow.New(leaflow.Options{
			Services:    services,
			ReadOnly:    readOnly,
			Endpoints:   ext.Config.Context().Addresses(),
			Credentials: ext.Tokens,
		})
		if err != nil {
			return err
		}

		server, err := mcp.New(mcp.Options{
			Client:  client,
			Version: version,
			Mode:    mcp.Mode(tools),
		})
		if err != nil {
			return err
		}

		// Resolved before anything is announced, so that a refused address is the
		// only thing printed rather than the second line after "starting".
		resolved := ""

		if address != "" {
			resolved, err = listenAddress(address, allowRemote)
			if err != nil {
				return err
			}
		}

		// Announced on stderr, never stdout: on the default transport stdout is
		// the protocol, and a friendly line there would be a parse error at the
		// other end. A client shows stderr in its logs, which is exactly where
		// someone looks when a server appears to have started but does nothing.
		log := cmd.ErrOrStderr()

		fmt.Fprintf(log, "leaflow mcp: %d tools (%s)", server.Tools(), tools)

		if readOnly {
			fmt.Fprint(log, ", read-only")
		}

		fmt.Fprintln(log)

		if resolved == "" {
			return server.Run(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
		}

		return serveHTTP(cmd.Context(), server.Handler(), resolved, log)
	}

	return cmd
}

// resolveAddress decides where --http listens, and defaults to this machine.
//
// A bare port binds loopback rather than every interface. The server holds a
// logged-in user's credentials and authenticates nobody, so the difference
// between `:8080` and `127.0.0.1:8080` is the difference between a local
// convenience and handing the platform to whoever else is on the network — and
// that is not a difference to decide by which spelling came to mind first.
func listenAddress(value string, allowRemote bool) (string, error) {
	value = strings.TrimSpace(value)

	if !strings.Contains(value, ":") {
		return net.JoinHostPort("127.0.0.1", value), nil
	}

	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %v", ErrBadEndpoint, value, err)
	}

	if host == "" {
		host = "127.0.0.1"
	}

	if allowRemote || isLocal(host) {
		return net.JoinHostPort(host, port), nil
	}

	return "", fmt.Errorf("%w: %s would accept connections from other machines, "+
		"and this server has no authentication of its own while holding yours. "+
		"Use a loopback address, or pass --allow-remote if something in front of it authenticates",
		ErrAddressNotLocal, value)
}

func isLocal(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

func serveHTTP(ctx context.Context, handler http.Handler, address string, log io.Writer) error {
	server := &http.Server{
		Handler: handler,
		// Set because this may face a network: without it a connection that opens
		// and then says nothing holds a goroutine indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Listening before serving, so that a port already in use is reported as an
	// error from this command rather than from a goroutine nobody is watching.
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}

	fmt.Fprintf(log, "listening on http://%s\n", listener.Addr())

	go func() {
		<-ctx.Done()

		// Bounded: in-flight calls are given a moment to finish, but Ctrl-C has to
		// end in the shell returning.
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()

		// Reported rather than dropped: a shutdown that timed out means a call was
		// still running when the process went away, and that is worth knowing when
		// the next thing in the log is a client complaining about a broken stream.
		if err := server.Shutdown(shutdown); err != nil {
			fmt.Fprintf(log, "shutdown: %v\n", err)
		}
	}()

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}
