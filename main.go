// Command closedvps buys a Hetzner Cloud server that the internet can't reach: a firewall with no inbound
// rules, SSH over Tailscale, optional Cloudflare Tunnel for web traffic.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// version is set by the release build.
var version = "dev"

// Exit codes. Scripts and agents rely on them, and `create --help` lists them.
const (
	exitOK      = 0
	exitFailed  = 1   // nothing bought, or any other error
	exitUsage   = 2   // unknown command or bad flags
	exitBilling = 3   // something was bought and is billing, but the run didn't finish
	exitStopped = 130 // Ctrl+C before anything was bought
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, newApp(), os.Args[1:])
	stop()
	code := exitCode(err)
	printError(os.Stderr, err, code)
	os.Exit(code)
}

// printError writes the error that ended a command, unless the command already wrote it: what went wrong,
// then what to do about it.
func printError(w io.Writer, err error, code int) {
	var f *failure
	switch {
	case code == exitOK:
	case errors.As(err, &f) && f.shown:
	case f != nil:
		newUI(w, false, time.Now).report(f)
	case code == exitStopped:
		fmt.Fprintln(w, "\n- Stopped")
	default:
		newUI(w, false, time.Now).fail("%s", upperFirst(err.Error()))
	}
}

// usageError is a mistake in how closedvps was called.
type usageError struct{ error }

func (e usageError) Unwrap() error { return e.error }

// exitCode checks billing first: a run stopped with Ctrl+C after buying must still say it's billing.
func exitCode(err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, new(billingError)):
		return exitBilling
	case errors.Is(err, context.Canceled):
		return exitStopped
	case errors.As(err, new(usageError)):
		return exitUsage
	}
	return exitFailed
}

// run is one closedvps command, followed by the note about a newer release when there is one.
func run(ctx context.Context, a *app, args []string) error {
	root := newRoot(a)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if a.updateNote != nil {
		if note := a.updateNote(); note != "" {
			fmt.Fprintln(a.stderr, "\n"+note)
		}
	}
	return err
}

func newRoot(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "closedvps",
		Short: "Buy a Hetzner Cloud server with every inbound port closed",
		Long: "closedvps buys a Hetzner Cloud server with every inbound port closed.\n\n" +
			"Once a day at most, a command run in a terminal asks closedvps.com whether a newer release is out and\n" +
			"says so when it ends. The site counts each ask by version, operating system and country, nothing\n" +
			"else. CLOSEDVPS_NO_UPDATE_CHECK=1 turns that off.\n\nDocs: https://closedvps.com",
		Version:       currentVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageError{fmt.Errorf("unknown command %q; see closedvps --help", args[0])}
			}
			return nil
		},
		RunE:             func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
		PersistentPreRun: func(cmd *cobra.Command, _ []string) { a.startUpdateCheck(cmd.Context(), cmd) },
	}
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
	root.AddCommand(newCreateCmd(a), newSetupCmd(a), newTypesCmd(a), newListCmd(a), newDeleteCmd(a),
		newOpenSSHCmd(a), newCloseSSHCmd(a), newForgetCmd(a), newUpgradeCmd(a), &cobra.Command{
			Use:   "version",
			Short: "Print the version",
			Args:  cobra.NoArgs,
			Run:   func(*cobra.Command, []string) { fmt.Fprintln(a.stdout, "closedvps", currentVersion()) },
		})
	for _, c := range root.Commands() {
		if c.Args != nil {
			args := c.Args
			c.Args = func(cmd *cobra.Command, as []string) error {
				if err := args(cmd, as); err != nil {
					return usageError{err}
				}
				return nil
			}
		}
	}
	return root
}

func currentVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}
