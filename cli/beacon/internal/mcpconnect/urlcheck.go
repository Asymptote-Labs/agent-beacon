package mcpconnect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/asymptote"
)

// ErrNoURL means neither --url nor a Beacon Managed connection gives an MCP URL.
var ErrNoURL = errors.New("no Beacon Managed MCP URL: run `beacon endpoint connect` to connect this endpoint to Beacon Managed, or pass --url")

// ResolveURL picks the MCP URL: the --url flag, else the ingest URL `beacon endpoint connect`
// recorded plus /mcp, which is where the Beacon Managed backend serves MCP by default. derived
// reports the second case; CheckURL then accepts the canonical URL the server names for itself.
func ResolveURL(flag, ingestURL string) (resolved string, derived bool, err error) {
	if v := strings.TrimSpace(flag); v != "" {
		return strings.TrimRight(v, "/"), false, nil
	}
	if v := strings.TrimSpace(ingestURL); v != "" {
		return strings.TrimRight(v, "/") + "/mcp", true, nil
	}
	return "", false, ErrNoURL
}

// protectedResource is the part of RFC 9728 resource metadata Beacon reads.
type protectedResource struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

// resourceMetadataURL is <origin>/.well-known/oauth-protected-resource, the document the server
// names in its 401 challenge.
func resourceMetadataURL(mcpURL string) (string, error) {
	u, err := url.Parse(mcpURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%q is not a URL", mcpURL)
	}
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource", nil
}

// CheckURL confirms that mcpURL is a Beacon Managed MCP server before anything is written: its
// origin must serve resource metadata whose "resource" is mcpURL. This is `beacon mcp connect`'s
// only network request (two when a derived URL is replaced by the canonical one), and nothing is
// sent with it -- no token, no cookie.
//
// A derived URL (the ingest URL plus /mcp) may be answered with a different canonical URL: the
// backend can serve MCP on its own host, and names that host as the resource. The canonical URL is
// then checked the same way and used instead. An explicit --url must match exactly, so a typo is
// reported rather than silently corrected.
func CheckURL(ctx context.Context, client *http.Client, mcpURL string, derived bool) (string, error) {
	resource, err := fetchResource(ctx, client, mcpURL)
	if err != nil {
		return "", err
	}
	if sameURL(resource, mcpURL) {
		return mcpURL, nil
	}
	if !derived {
		return "", fmt.Errorf("%s is not the MCP URL this server advertises; it names %s. Pass --url %s", mcpURL, resource, resource)
	}
	canonical := strings.TrimRight(resource, "/")
	again, err := fetchResource(ctx, client, canonical)
	if err != nil {
		return "", fmt.Errorf("the server at %s names %s as its MCP URL, but checking that failed: %w", mcpURL, canonical, err)
	}
	if !sameURL(again, canonical) {
		return "", fmt.Errorf("the server at %s names %s as its MCP URL, and that names %s; pass --url explicitly", mcpURL, canonical, again)
	}
	return canonical, nil
}

func fetchResource(ctx context.Context, client *http.Client, mcpURL string) (string, error) {
	if !asymptote.IsSecureURL(mcpURL) {
		return "", fmt.Errorf("%s must use https:// (plain http is allowed only for a loopback development server)", mcpURL)
	}
	metadataURL, err := resourceMetadataURL(mcpURL)
	if err != nil {
		return "", err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	// Never follow a redirect: the check is about this origin, and a redirect could send the
	// request somewhere the person did not name.
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach %s: %w", metadataURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s answered HTTP %d; it does not look like a Beacon Managed MCP server", metadataURL, resp.StatusCode)
	}
	var meta protectedResource
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&meta); err != nil {
		return "", fmt.Errorf("%s did not return resource metadata JSON: %w", metadataURL, err)
	}
	if strings.TrimSpace(meta.Resource) == "" {
		return "", fmt.Errorf("%s has no \"resource\"", metadataURL)
	}
	if !asymptote.IsSecureURL(meta.Resource) {
		return "", fmt.Errorf("%s names an insecure resource %q", metadataURL, meta.Resource)
	}
	return meta.Resource, nil
}
