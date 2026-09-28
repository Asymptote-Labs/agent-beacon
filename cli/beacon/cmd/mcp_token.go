package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/account"
)

// `beacon mcp token create` mints a personal Beacon Managed MCP token with the signed-in account.
// The token goes to stdout exactly once, so it can be captured into a variable for
// `beacon mcp connect --token-env`; everything else goes to stderr. It is never written to a
// file, and the account token that authorizes the request is never printed.

const defaultMCPTokenExpiryDays = 90

var (
	mcpTokenAccountLoad = account.Load
	mcpTokenCreate      = account.CreateMCPToken
	mcpTokenHostname    = os.Hostname
	mcpTokenHTTPClient  = &http.Client{Timeout: 30 * time.Second}
)

var mcpTokenOpts struct {
	name          string
	expiresInDays int
	json          bool
}

var mcpTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage personal Beacon Managed MCP tokens",
}

var mcpTokenCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a personal Beacon Managed MCP token with your Beacon sign-in",
	Long: `Create a personal Beacon Managed MCP token for the account signed in with ` + "`beacon login`" + `.

The token is printed to stdout once and cannot be shown again; progress and details go to
stderr. Capture it into a variable and reference that variable with
` + "`beacon mcp connect --token-env`" + `:

  export BEACON_MCP_TOKEN="$(beacon mcp token create --name laptop)"
  beacon mcp connect --token-env BEACON_MCP_TOKEN

The token reads your Beacon Managed data and counts toward the limit of 25 active tokens.
Revoke it at beacon.sh → Dashboard → MCP Access. Beacon never writes it to a file.`,
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE:         runMCPTokenCreate,
}

func init() {
	mcpCmd.AddCommand(mcpTokenCmd)
	mcpTokenCmd.AddCommand(mcpTokenCreateCmd)
	f := mcpTokenCreateCmd.Flags()
	f.StringVar(&mcpTokenOpts.name, "name", "", "Token name shown in the dashboard (default: beacon-cli on this machine's hostname)")
	f.IntVar(&mcpTokenOpts.expiresInDays, "expires-in-days", defaultMCPTokenExpiryDays, fmt.Sprintf("Days until the token expires, 1-%d, or 0 for never", account.MaxMCPTokenExpiryDays))
	f.BoolVar(&mcpTokenOpts.json, "json", false, "Print the token and its details as JSON on stdout")
}

func runMCPTokenCreate(cmd *cobra.Command, args []string) error {
	name := mcpTokenOpts.name
	if name == "" {
		name = "beacon-cli"
		if host, err := mcpTokenHostname(); err == nil && host != "" {
			name = "beacon-cli on " + host
		}
	}
	req := account.MCPTokenRequest{Name: name, ExpiresInDays: mcpTokenOpts.expiresInDays}
	if err := account.ValidateMCPTokenRequest(req); err != nil {
		return err
	}
	session, err := mcpTokenAccountLoad()
	if errors.Is(err, account.ErrNotSignedIn) {
		return errors.New("not signed in to Beacon: run `beacon login` first")
	}
	if err != nil {
		return err
	}
	if session.Expired(time.Now()) {
		return errors.New("Beacon sign-in expired: run `beacon login` again")
	}
	token, err := mcpTokenCreate(commandContext(cmd), *session, req, mcpTokenHTTPClient)
	if err != nil {
		return err
	}
	if mcpTokenOpts.json {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(token)
	}
	fmt.Fprintln(cmd.OutOrStdout(), token.Token)
	errOut := cmd.ErrOrStderr()
	fmt.Fprintf(errOut, "Created MCP token %q (%s).\n", token.Info.Name, token.Info.Prefix)
	if token.Info.ExpiresAt != nil {
		fmt.Fprintf(errOut, "Expires: %s\n", token.Info.ExpiresAt.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintln(errOut, "Expires: never")
	}
	if token.MCPURL != "" {
		fmt.Fprintf(errOut, "MCP URL: %s\n", token.MCPURL)
	}
	fmt.Fprintln(errOut, "The token is shown only once. Revoke it at beacon.sh → Dashboard → MCP Access.")
	return nil
}
