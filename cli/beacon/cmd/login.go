package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/auth"
)

type loginOptions struct {
	dashboardURL string
	force        bool
}

type loginService interface {
	Login(auth.LoginOptions) (*auth.Credentials, error)
	LoadCredentials() (*auth.Credentials, error)
	SaveCredentials(*auth.Credentials) error
	IsLoggedIn() bool
}

type defaultLoginService struct{}

func (defaultLoginService) Login(opts auth.LoginOptions) (*auth.Credentials, error) {
	return auth.Login(opts)
}

func (defaultLoginService) LoadCredentials() (*auth.Credentials, error) {
	return auth.LoadCredentials()
}

func (defaultLoginService) SaveCredentials(creds *auth.Credentials) error {
	return auth.SaveCredentials(creds)
}

func (defaultLoginService) IsLoggedIn() bool {
	return auth.IsLoggedIn()
}

var (
	loginOpts = loginOptions{}

	newLoginService = func() loginService {
		return defaultLoginService{}
	}
)

var loginCmd = &cobra.Command{
	Use:          "login",
	Short:        "Log in to the Asymptote dashboard",
	Long:         "Log in to the Asymptote dashboard using your browser. Beacon endpoint telemetry remains local-only unless you explicitly use cloud features.",
	SilenceUsage: true,
	Args:         cobra.NoArgs,
	RunE:         runLogin,
}

func init() {
	loginCmd.Flags().StringVar(&loginOpts.dashboardURL, "dashboard-url", "", "Asymptote dashboard URL (defaults to "+auth.DefaultDashboardURL+", or "+auth.DashboardURLEnv+")")
	loginCmd.Flags().BoolVar(&loginOpts.force, "force", false, "Replace existing saved credentials")
	rootCmd.AddCommand(loginCmd)
}

func runLogin(cmd *cobra.Command, args []string) error {
	service := newLoginService()
	out := cmd.OutOrStdout()

	if !loginOpts.force && service.IsLoggedIn() {
		creds, err := service.LoadCredentials()
		if err != nil {
			return fmt.Errorf("failed to load saved credentials: %w", err)
		}
		if creds != nil {
			if creds.Email != "" {
				fmt.Fprintf(out, "Already logged in as %s\n", creds.Email)
			} else {
				fmt.Fprintln(out, "Already logged in.")
			}
			if creds.OrgName != "" {
				fmt.Fprintf(out, "Organization: %s\n", creds.OrgName)
			}
			fmt.Fprintln(out, "Run 'beacon login --force' to log in as a different user.")
			return nil
		}
	}

	creds, err := service.Login(auth.LoginOptions{
		DashboardURL: loginOpts.dashboardURL,
		Out:          out,
	})
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	if err := service.SaveCredentials(creds); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	fmt.Fprintln(out)
	if creds.Email != "" {
		fmt.Fprintf(out, "Success! Logged in as %s\n", creds.Email)
	} else {
		fmt.Fprintln(out, "Success! Logged in successfully.")
	}
	if creds.OrgName != "" {
		fmt.Fprintf(out, "Organization: %s\n", creds.OrgName)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Beacon endpoint telemetry remains local-only unless you explicitly use cloud features.")
	return nil
}
