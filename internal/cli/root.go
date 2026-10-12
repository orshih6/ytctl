// Package cli implements ytctl's command-line interface.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/orshih6/ytctl/internal/auth"
	"github.com/orshih6/ytctl/internal/config"
	"github.com/orshih6/ytctl/internal/output"
	"github.com/orshih6/ytctl/internal/yt"
)

// interruptExitCode is the conventional shell status for a command killed
// by SIGINT (128 + 2).
const interruptExitCode = 130

var (
	flagConfigPath       string
	flagClientSecretFile string
	flagJSON             bool
	flagQuiet            bool
	flagYes              bool
)

type configKey struct{}

func configFrom(cmd *cobra.Command) config.Config {
	cfg, _ := cmd.Context().Value(configKey{}).(config.Config)
	return cfg
}

// newClient builds the API client for a command from the stored sign-in.
// A variable so tests can substitute a client aimed at a local server.
var newClient = func(cmd *cobra.Command) (*yt.Client, error) {
	cfg := configFrom(cmd)
	if err := cfg.RequireClient(); err != nil {
		return nil, err
	}
	ctx := cmd.Context()
	src, err := auth.TokenSource(ctx, auth.NewConfig(cfg.ClientID, cfg.ClientSecret), cfg.TokenPath)
	if err != nil {
		return nil, err
	}
	return yt.New(ctx, oauth2.NewClient(ctx, src), "")
}

// progressWriter is where an upload reports itself: stderr, so that a
// command's real output stays capturable, and nothing at all under --quiet.
func progressWriter(cmd *cobra.Command) io.Writer {
	if flagQuiet {
		return io.Discard
	}
	return cmd.ErrOrStderr()
}

// emit prints a command's result: the API resource as JSON under --json,
// otherwise whatever text does.
func emit(cmd *cobra.Command, resource any, text func(w io.Writer) error) error {
	if flagJSON {
		return output.JSON(cmd.OutOrStdout(), resource)
	}
	return text(cmd.OutOrStdout())
}

// confirm asks before something that cannot be undone, unless --yes was
// given.
func confirm(cmd *cobra.Command, question string) error {
	if flagYes {
		return nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "%s [y/N] ", question)
	answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return errors.New("aborted (pass --yes to skip the question)")
}

// buildVersion reports the version stamped into the binary. Built with
// `go build`, that comes from the module's VCS metadata; a release built
// with -ldflags "-X ...version=v1.2.3" overrides it.
var version string

func buildVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			if len(setting.Value) > 12 {
				return setting.Value[:12]
			}
			return setting.Value
		}
	}
	return "unknown"
}

func defaultConfigPath() string {
	dir := config.DefaultDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "config.yaml")
}

func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ytctl",
		Short:   "Manage a YouTube channel from the command line",
		Version: buildVersion(),
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Resolve(flagClientSecretFile, flagConfigPath)
			if err != nil {
				return err
			}
			cmd.SetContext(context.WithValue(cmd.Context(), configKey{}, cfg))
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.PersistentFlags().StringVar(&flagConfigPath, "config", defaultConfigPath(), "path to config file")
	cmd.PersistentFlags().StringVar(&flagClientSecretFile, "client-secret-file", "", "OAuth client JSON downloaded from the Google Cloud console")
	cmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "print the API's response as JSON")
	cmd.PersistentFlags().BoolVarP(&flagQuiet, "quiet", "q", false, "suppress progress reporting")
	cmd.PersistentFlags().BoolVarP(&flagYes, "yes", "y", false, "do not ask before deleting")

	cmd.AddCommand(
		newAuthCommand(),
		newChannelCommand(),
		newVideoCommand(),
		newPlaylistCommand(),
		newCommentCommand(),
		newUploadCommand(),
	)

	return cmd
}

// Execute runs the ytctl root command, exiting the process on error.
//
// The command's context is cancelled on SIGINT or SIGTERM so that an
// upload or a pending sign-in can be stopped deliberately. An interrupt is
// reported as such rather than as a failure.
func Execute() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := newRootCommand().ExecuteContext(ctx)
	if err == nil {
		return
	}

	if ctx.Err() != nil {
		fmt.Fprintln(os.Stderr, "interrupted")
		os.Exit(interruptExitCode)
	}

	fmt.Fprintln(os.Stderr, "ytctl:", err)
	os.Exit(1)
}

// oneOf rejects a flag value outside the set the API accepts, so that a
// typo is reported by name instead of as an HTTP 400.
func oneOf(flag, value string, allowed ...string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("invalid --%s %q: must be one of %s", flag, value, strings.Join(allowed, ", "))
}

func checkLimit(n, max int64) error {
	if n < 1 || n > max {
		return fmt.Errorf("invalid --limit %d: must be between 1 and %d", n, max)
	}
	return nil
}

var privacyStatuses = []string{"public", "unlisted", "private"}
