package cli

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/orshih6/ytctl/internal/auth"
	"github.com/orshih6/ytctl/internal/output"
)

func newAuthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Sign in to and out of a Google account",
	}
	cmd.AddCommand(newAuthLoginCommand(), newAuthStatusCommand(), newAuthLogoutCommand())
	return cmd
}

func newAuthLoginCommand() *cobra.Command {
	var noBrowser bool

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in through the browser and store the token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd)
			if err := cfg.RequireClient(); err != nil {
				return err
			}

			stderr := cmd.ErrOrStderr()
			tok, err := auth.Login(cmd.Context(), auth.NewConfig(cfg.ClientID, cfg.ClientSecret), func(url string) {
				fmt.Fprintf(stderr, "Open this URL to sign in:\n\n  %s\n\nWaiting for the browser...\n", url)
				if !noBrowser {
					openBrowser(url)
				}
			})
			if err != nil {
				return err
			}
			if tok.RefreshToken == "" {
				return errors.New("Google returned no refresh token, so the sign-in would expire within the hour; remove ytctl's access at https://myaccount.google.com/permissions and log in again")
			}
			if err := auth.SaveToken(cfg.TokenPath, tok); err != nil {
				return err
			}
			fmt.Fprintf(stderr, "Signed in. Token saved to %s\n", cfg.TokenPath)

			// Naming the channel confirms the right account was picked,
			// but the sign-in has already succeeded either way.
			if client, err := newClient(cmd); err == nil {
				if ch, err := client.MyChannel(cmd.Context()); err == nil {
					fmt.Fprintf(stderr, "Channel: %s (%s)\n", ch.Snippet.Title, ch.Id)
				} else {
					fmt.Fprintf(stderr, "Could not look up the channel: %v\n", err)
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the sign-in URL without opening a browser")
	return cmd
}

func newAuthStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which channel ytctl is signed in to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd)
			if _, err := auth.LoadToken(cfg.TokenPath); err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			ch, err := client.MyChannel(cmd.Context())
			if err != nil {
				return err
			}

			// Read again: looking the channel up may have refreshed it.
			tok, err := auth.LoadToken(cfg.TokenPath)
			if err != nil {
				return err
			}
			status := map[string]any{
				"channelId":         ch.Id,
				"channelTitle":      ch.Snippet.Title,
				"tokenFile":         cfg.TokenPath,
				"accessTokenExpiry": tok.Expiry.UTC().Format(time.RFC3339),
			}
			return emit(cmd, status, func(w io.Writer) error {
				return output.Fields(w, []output.Field{
					{Name: "Channel", Value: ch.Snippet.Title},
					{Name: "Channel ID", Value: ch.Id},
					{Name: "Token file", Value: cfg.TokenPath},
					{Name: "Access token", Value: "valid until " + tok.Expiry.Local().Format("15:04") + ", refreshed automatically"},
				})
			})
		},
	}
}

func newAuthLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Revoke the token and delete it from disk",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFrom(cmd)
			stderr := cmd.ErrOrStderr()

			tok, err := auth.LoadToken(cfg.TokenPath)
			if errors.Is(err, auth.ErrNotLoggedIn) {
				fmt.Fprintln(stderr, "Not signed in.")
				return nil
			}
			if err == nil {
				// The local token goes regardless: a failed revocation
				// must not leave the user unable to sign out.
				if err := auth.Revoke(cmd.Context(), tok); err != nil {
					fmt.Fprintf(stderr, "Warning: %v. Remove ytctl's access at https://myaccount.google.com/permissions\n", err)
				}
			}
			if err := auth.DeleteToken(cfg.TokenPath); err != nil {
				return err
			}
			fmt.Fprintln(stderr, "Signed out.")
			return nil
		},
	}
}

// openBrowser makes a best-effort attempt to open url. The URL has already
// been printed, so a failure here needs no report.
func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	if c.Start() == nil {
		go c.Wait()
	}
}
