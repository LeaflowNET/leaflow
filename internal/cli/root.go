// Package cli assembles the kernel, the contracts and the extensions into one
// program. It is the only package that knows about all of them: spec knows
// nothing of cobra, and transport nothing of the command tree.
package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/LeaflowNET/leaflow/internal/auth"
	"github.com/LeaflowNET/leaflow/internal/builtin"
	"github.com/LeaflowNET/leaflow/internal/config"
	"github.com/LeaflowNET/leaflow/internal/dynamic"
	"github.com/LeaflowNET/leaflow/internal/extension"
	"github.com/LeaflowNET/leaflow/internal/output"
	"github.com/LeaflowNET/leaflow/internal/spec"
	"github.com/LeaflowNET/leaflow/internal/transport"
)

// Injected at build time with -ldflags -X.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

// App is one run's state. Keeping it in a struct rather than package variables
// lets tests build several that do not leak flags into each other.
type App struct {
	Out io.Writer
	Err io.Writer
	In  io.Reader

	// Filled in PersistentPreRunE: the tree is built before flags are parsed, so
	// commands hold pointers to these and read them when they run.
	ext *extension.Context
	rt  *dynamic.Runtime

	configPath  string
	contextName string
	projectID   string
	outputName  string
	verbose     bool
}

func Execute(args []string) int {
	app := &App{Out: os.Stdout, Err: os.Stderr, In: os.Stdin}

	return app.Run(args)
}

func (a *App) Run(args []string) int {
	root, err := a.Root()
	if err != nil {
		return a.report(err, output.FormatTable)
	}

	root.SetArgs(args)
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	root.SetIn(a.In)

	// Ctrl-C cancels through the context so in-flight requests and the login
	// poll can stop cleanly rather than being killed mid-flow.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := root.ExecuteContext(ctx); err != nil {
		return a.report(err, a.errorFormat())
	}

	return ExitOK
}

// errorFormat decides how a failure is rendered. It re-parses the flag instead
// of reading the printer, because a failure during setup happens before the
// printer exists — and that is exactly when a wrapper still needs JSON.
func (a *App) errorFormat() output.Format {
	if a.outputName == "" {
		return output.FormatTable
	}

	format, _, err := output.ParseFormat(a.outputName)
	if err != nil {
		return output.FormatTable
	}

	return format
}

func (a *App) Root() (*cobra.Command, error) {
	// Contracts are embedded and independent of configuration, which is required:
	// the command tree is built from them.
	specs, err := spec.Load()
	if err != nil {
		return nil, err
	}

	a.ext = &extension.Context{Specs: specs}
	a.rt = &dynamic.Runtime{}

	root := &cobra.Command{
		Use:   "leaflow",
		Short: "Leaflow platform command line",
		Long: strings.TrimSpace(`
Leaflow platform command line.

Commands are grouped by service, generated from each service's OpenAPI
contract:

  leaflow compute disk create-disk

Both name parts come from the contract: the tag and the operationId. The same
identifier names the operation in the SDKs, so there is no second vocabulary to
learn or to keep in sync.

Contracts ship with this binary and can be refreshed: leaflow spec update`),
		Version:       versionString(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	a.registerGlobalFlags(root)

	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		return a.setup(cmd)
	}

	for _, cmd := range builtin.Commands(a.ext) {
		root.AddCommand(cmd)
	}

	for _, cmd := range dynamic.Build(specs, a.rt, a.ext) {
		root.AddCommand(cmd)
	}

	return root, nil
}

func (a *App) registerGlobalFlags(root *cobra.Command) {
	flags := root.PersistentFlags()

	flags.StringVar(&a.configPath, "config", "",
		"config file (default ~/.config/leaflow/config.yaml; or LEAFLOW_CONFIG)")
	flags.StringVar(&a.contextName, "context", "",
		"context to use (or LEAFLOW_CONTEXT)")
	flags.StringVar(&a.projectID, "project", "",
		"project for this run, overriding the config (or LEAFLOW_PROJECT)")
	flags.StringVarP(&a.outputName, "output", "o", "",
		"output format: "+strings.Join(output.Formats, ", ")+". Use json in scripts; table layout may change")
	flags.BoolVar(&a.verbose, "verbose", false, "log the requests being made")
}

// setup builds the kernel once flags are known.
//
// Doing it here rather than in Root() also means `leaflow --help` and
// `leaflow spec list` need neither configuration nor network, so a malformed
// config file cannot stop them.
func (a *App) setup(cmd *cobra.Command) error {
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return err
	}

	// Flags beat the environment, which beats the file: the command line is the
	// most specific statement of intent for this run.
	if a.contextName != "" {
		cfg.Current = a.contextName
	}

	// Applied to the stored context but never saved: a flag states intent for
	// this run, and everything downstream reads its own copy through
	// cfg.Context(), so setting it on a local copy here would have no effect.
	if a.projectID != "" {
		cfg.EditContext("").Project = a.projectID
	}

	requested := cfg.Output
	if a.outputName != "" {
		requested = a.outputName
	}

	if requested == "" {
		requested = string(output.FormatTable)
	}

	format, columns, err := output.ParseFormat(requested)
	if err != nil {
		return err
	}

	httpClient := &http.Client{Timeout: 60 * time.Second}

	tokens, err := auth.NewManager(cfg, httpClient)
	if err != nil {
		return err
	}

	if account, ok := a.ext.Specs.Service("account"); ok {
		if exchange, found := account.Operation(auth.ExchangeOperation); found {
			tokens.UseExchangePath(exchange.Path)
		}
	}

	client := transport.New(cfg, tokens, httpClient)
	client.Verbose = a.verbose
	client.Log = cmd.ErrOrStderr()

	printer := &output.Printer{
		Format:  format,
		Columns: columns,
		Out:     cmd.OutOrStdout(),
		Width:   terminalWidth(cmd.OutOrStdout()),
		Notice:  cmd.ErrOrStderr(),
	}

	a.rt.Client = client
	a.rt.Printer = printer

	a.ext.Config = cfg
	a.ext.Tokens = tokens
	a.ext.Client = client
	a.ext.Printer = printer

	return nil
}

// terminalWidth returns 0 when stdout is not a terminal — a pipe has no width,
// and a table written into one should not be trimmed to fit a screen nobody is
// looking at.
func terminalWidth(out io.Writer) int {
	file, ok := out.(*os.File)
	if !ok {
		return 0
	}

	width, _, err := term.GetSize(int(file.Fd()))
	if err != nil {
		return 0
	}

	return width
}

func versionString() string {
	switch {
	case commit == "":
		return version
	case date == "":
		return fmt.Sprintf("%s (%s)", version, commit)
	default:
		return fmt.Sprintf("%s (%s, %s)", version, commit, date)
	}
}
