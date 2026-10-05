// Command gen regenerates cli/beacon/internal/pricing/catalog.json from LiteLLM's published
// price list (https://github.com/BerriAI/litellm, MIT License).
//
// It is run by hand when prices need refreshing, from cli/beacon:
//
//	go run ./internal/pricing/internal/gen
//
// With no -src it resolves the head of LiteLLM's main branch with `git ls-remote`, downloads
// the list at that exact commit, and records the commit in the catalog, so the file says
// precisely which upstream revision it reflects. -src reads a local copy instead (pass -commit
// to record where it came from). Nothing here runs during a build or a test, and the beacon
// binary never fetches prices: the output is embedded.
//
// The output is deterministic for a given input and dates: models are sorted, one per line, so
// a regeneration diff shows exactly which prices moved.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	upstreamRepo = "https://github.com/BerriAI/litellm"
	upstreamPath = "model_prices_and_context_window.json"
	upstreamURL  = "https://raw.githubusercontent.com/BerriAI/litellm/main/" + upstreamPath
)

func main() {
	src := flag.String("src", "", "local copy of "+upstreamPath+" (default: download it at the head of LiteLLM's main branch)")
	commit := flag.String("commit", "", "upstream commit the -src copy was taken from, recorded in the catalog")
	out := flag.String("out", "internal/pricing/catalog.json", "catalog file to write")
	fetched := flag.String("fetched", "", "date the list was read, YYYY-MM-DD (default: today, UTC)")
	generated := flag.String("generated", "", "generation date, YYYY-MM-DD (default: today, UTC)")
	strict := flag.Bool("strict", false, "fail if any rate does not convert exactly to integer microdollars per million tokens")
	flag.Parse()

	today := time.Now().UTC().Format("2006-01-02")
	if *fetched == "" {
		*fetched = today
	}
	if *generated == "" {
		*generated = today
	}

	raw, sha, err := readUpstream(*src, *commit)
	if err != nil {
		fatal(err)
	}
	data, rep, err := generate(raw, sourceInfo(sha, *fetched), *generated)
	if err != nil {
		fatal(err)
	}
	rep.print(os.Stderr)
	if *strict && len(rep.Rounded) > 0 {
		fatal(fmt.Errorf("%d rate(s) were rounded; see above", len(rep.Rounded)))
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes, %d models)\n", *out, len(data), rep.Kept)
}

func readUpstream(src, commit string) ([]byte, string, error) {
	if src != "" {
		data, err := os.ReadFile(src)
		return data, commit, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if commit == "" {
		resolved, err := resolveHead(ctx)
		if err != nil {
			return nil, "", err
		}
		commit = resolved
	}
	url := "https://raw.githubusercontent.com/BerriAI/litellm/" + commit + "/" + upstreamPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	return data, commit, err
}

// resolveHead asks git for the commit at the head of LiteLLM's main branch. git rather than the
// GitHub API because it needs no token and is not rate limited for this.
func resolveHead(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "ls-remote", upstreamRepo, "refs/heads/main").Output()
	if err != nil {
		return "", fmt.Errorf("resolve %s main: %w", upstreamRepo, err)
	}
	fields := strings.Fields(string(bytes.TrimSpace(out)))
	if len(fields) == 0 || len(fields[0]) != 40 {
		return "", errors.New("resolve LiteLLM main: unexpected git ls-remote output")
	}
	return fields[0], nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gen:", err)
	os.Exit(1)
}
