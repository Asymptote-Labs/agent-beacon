package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
)

var accountOpts struct {
	baseURL   string
	noBrowser bool
	json      bool
}

var (
	accountLogin   = account.Login
	accountSave    = account.Save
	accountLoad    = account.Load
	accountRemove  = account.Remove
	accountInspect = account.Inspect
	accountRevoke  = account.Revoke
	accountNow     = time.Now
	// accountPasteInput is where `beacon login` reads a pasted callback address.
	// Only a terminal qualifies: piped stdin is not a person who can paste.
	accountPasteInput = func(cmd *cobra.Command) io.Reader {
		if !isTerminal(os.Stdin) {
			return nil
		}
		return cmd.InOrStdin()
	}
)

var loginCmd = &cobra.Command{
	Use:          "login",
	Short:        "Sign in to Beacon through beacon.sh",
	SilenceUsage: true,
	RunE:         runLogin,
}

var logoutCmd = &cobra.Command{
	Use:          "logout",
	Short:        "Sign out and remove the local Beacon session",
	SilenceUsage: true,
	RunE:         runLogout,
}

var whoamiCmd = &cobra.Command{
	Use:          "whoami",
	Short:        "Show the current Beacon account",
	SilenceUsage: true,
	RunE:         runWhoami,
}

func init() {
	loginCmd.Flags().StringVar(&accountOpts.baseURL, "auth-url", "", "Beacon authentication URL (defaults to "+account.DefaultBaseURL+", or "+account.BaseURLEnv+")")
	loginCmd.Flags().BoolVar(&accountOpts.noBrowser, "no-browser", false, "Print the sign-in URL instead of opening a browser; on a remote machine, finish by pasting the address the browser was sent to")
	loginCmd.Flags().BoolVar(&accountOpts.noBrowser, "headless", false, "Alias for --no-browser, for signing in on a remote or headless machine")
	for _, command := range []*cobra.Command{loginCmd, logoutCmd, whoamiCmd} {
		command.Flags().BoolVar(&accountOpts.json, "json", false, "Print machine-readable JSON")
		rootCmd.AddCommand(command)
	}
}

func runLogin(cmd *cobra.Command, args []string) error {
	flowOut := cmd.OutOrStdout()
	if accountOpts.json {
		flowOut = cmd.ErrOrStderr()
	}
	session, err := accountLogin(commandContext(cmd), account.LoginOptions{
		BaseURL:    accountOpts.baseURL,
		Version:    version.GetVersion(),
		NoBrowser:  accountOpts.noBrowser,
		Out:        flowOut,
		Now:        accountNow,
		PasteInput: accountPasteInput(cmd),
	})
	if err != nil {
		return err
	}
	if err := accountSave(*session); err != nil {
		return fmt.Errorf("store Beacon session: %w", err)
	}
	status := accountInspect(accountNow())
	if accountOpts.json {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Signed in to Beacon as %s.\n", displayAccountUser(status.User))
	if status.ActiveOrganization != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Organization: %s\n", displayOrganization(*status.ActiveOrganization))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Session: %s (0600; access token is never printed)\n", status.SessionPath)
	return nil
}

func runLogout(cmd *cobra.Command, args []string) error {
	session, loadErr := accountLoad()
	var revokeErr error
	if loadErr == nil {
		ctx, cancel := context.WithTimeout(commandContext(cmd), 10*time.Second)
		revokeErr = accountRevoke(ctx, *session, &http.Client{Timeout: 10 * time.Second})
		cancel()
	} else if !errors.Is(loadErr, account.ErrNotSignedIn) {
		fmt.Fprintf(cmd.ErrOrStderr(), "beacon: could not read the existing session before logout: %v\n", loadErr)
	}
	if err := accountRemove(); err != nil {
		return fmt.Errorf("remove local Beacon session: %w", err)
	}
	if revokeErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "beacon: signed out locally; server revocation could not be confirmed: %v\n", revokeErr)
	}
	if accountOpts.json {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"signed_out":         true,
			"revocation_pending": revokeErr != nil,
		})
	}
	if errors.Is(loadErr, account.ErrNotSignedIn) {
		fmt.Fprintln(cmd.OutOrStdout(), "Not signed in.")
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Signed out of Beacon. Local credentials removed.")
	return nil
}

func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func runWhoami(cmd *cobra.Command, args []string) error {
	status := accountInspect(accountNow())
	if accountOpts.json {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	}
	if !status.SignedIn {
		fmt.Fprintln(cmd.OutOrStdout(), "Not signed in. Run `beacon login`.")
		fmt.Fprintf(cmd.OutOrStdout(), "Session: %s\n", status.SessionPath)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Signed in as %s\n", displayAccountUser(status.User))
	if status.ActiveOrganization != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Organization: %s\n", displayOrganization(*status.ActiveOrganization))
	} else if len(status.Organizations) > 0 {
		names := make([]string, 0, len(status.Organizations))
		for _, organization := range status.Organizations {
			names = append(names, displayOrganization(organization))
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Organizations: %s\n", strings.Join(names, ", "))
	}
	if !status.ExpiresAt.IsZero() {
		label := "expires"
		if status.Expired {
			label = "expired"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Session %s: %s\n", label, status.ExpiresAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Authentication service: %s\n", status.BaseURL)
	fmt.Fprintf(cmd.OutOrStdout(), "Session: %s\n", status.SessionPath)
	return nil
}

func displayAccountUser(user account.User) string {
	switch {
	case user.Name != "" && user.Email != "":
		return fmt.Sprintf("%s <%s>", user.Name, user.Email)
	case user.Email != "":
		return user.Email
	default:
		return user.ID
	}
}

func displayOrganization(organization account.Organization) string {
	if organization.Name != "" {
		return organization.Name
	}
	if organization.Slug != "" {
		return organization.Slug
	}
	return organization.ID
}
