package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/detect"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/threatrules"
)

var rulesOpts struct {
	userMode   bool
	systemMode bool
	force      bool
	markdown   bool
	sha256     string
}

func rulesUserMode() bool {
	if rulesOpts.systemMode {
		return false
	}
	return rulesOpts.userMode
}

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "Manage and author Beacon threat-detection rules",
	Long: `Manage the local threat-rule store (~/.beacon/endpoint/rules) that 'beacon scan'
runs, and author rules.

The detection engine ships in the binary; rules are external data loaded from the store,
so a growing rule corpus never enlarges the binary. A small baseline is built in and
always active: a store rule replaces a baseline rule only when it has the same id.`,
}

var rulesListCmd = &cobra.Command{
	Use:          "list",
	Short:        "List the active threat-detection rules",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		loaded, skipped, err := detect.LoadActiveSkipping(rulesUserMode(), "")
		if err != nil {
			return err
		}
		detect.WarnSkipped(cmd.ErrOrStderr(), skipped)
		out := cmd.OutOrStdout()
		if len(loaded) == 0 {
			fmt.Fprintln(out, "No rules.")
			return nil
		}
		for _, lr := range loaded {
			fmt.Fprintf(out, "%-32s %-8s %-12s %s\n", lr.Rule.ID, lr.Rule.Severity, lr.Rule.Status, lr.Source)
		}
		fromStore := 0
		for _, lr := range loaded {
			if lr.Source == detect.SourceStore {
				fromStore++
			}
		}
		fmt.Fprintf(out, "\n%d rule(s): %d from the store, %d from the built-in baseline. Store: %s\n",
			len(loaded), fromStore, len(loaded)-fromStore, detect.StoreDir(rulesUserMode()))
		return nil
	},
}

var rulesAddCmd = &cobra.Command{
	Use:   "add <path>",
	Short: "Install rule file(s) from a local path into the store",
	Long: `Installs the rule file, or every rule file under the directory, into the store, with
the definitions found beside them (*.catalog.yaml and *.predicates.yaml: what the predicates
the rules call mean). A rule that calls a predicate needs its pack's definitions, so add the
pack's directory rather than one of its rule files, unless the store already has them.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		installed, skipped, err := detect.InstallFilesSkipping(rulesUserMode(), args[0], rulesOpts.force)
		if err != nil {
			return err
		}
		detect.WarnSkipped(cmd.ErrOrStderr(), skipped)
		printInstalled(cmd.OutOrStdout(), installed)
		return nil
	},
}

// printInstalled reports what an install wrote: each rule and each definitions file, then the
// counts.
func printInstalled(out io.Writer, installed []detect.Installed) {
	rules, definitions := 0, 0
	for _, in := range installed {
		if in.Definitions {
			definitions++
			fmt.Fprintf(out, "installed definitions %s -> %s\n", in.ID, in.Path)
			continue
		}
		rules++
		fmt.Fprintf(out, "installed %s -> %s\n", in.ID, in.Path)
	}
	if definitions > 0 {
		fmt.Fprintf(out, "%d rule(s) installed, with %d definitions file(s).\n", rules, definitions)
		return
	}
	fmt.Fprintf(out, "%d rule(s) installed.\n", rules)
}

var rulesRemoveCmd = &cobra.Command{
	Use:          "remove <id>",
	Short:        "Remove a rule from the store by id",
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := detect.Remove(rulesUserMode(), args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "removed %s (%s)\n", args[0], path)
		if detect.IsBaselineID(args[0]) {
			fmt.Fprintf(cmd.OutOrStdout(), "%s is also a built-in baseline rule; the baseline copy is active again.\n", args[0])
		}
		return nil
	},
}

var rulesPullCmd = &cobra.Command{
	Use:   "pull <url>",
	Short: "Fetch a rule pack from a URL into the store (reaches the network)",
	Long: `Downloads a rule file (.rule.yaml) or a gzipped tarball (.tar.gz/.tgz) of rules
from an explicit URL and installs the valid rules into the store, with the definitions
(*.catalog.yaml, *.predicates.yaml) a tarball carries beside them.

This is the only Beacon command that reaches the network, and only when you run it. The
agent never fetches rules on its own. The URL is whatever you supply; there is no hosted
default.

The URL must be https (plain http is accepted only for a loopback host, for local
testing), and a redirect that leaves https is refused. Pass --sha256 with the pack's
published checksum to verify the downloaded bytes before anything is installed. The
download is capped at 32 MiB, and a tarball at 10,000 entries and 64 MiB uncompressed.`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runRulesPull,
}

// Limits on what `beacon rules pull` will download and unpack. The published corpus
// tarball is about 50 KB with under 100 entries and 300 KB uncompressed, so each limit
// leaves two orders of magnitude of headroom while keeping a hostile or broken source
// from filling the disk or $TMPDIR. They are variables only so tests can lower them.
var (
	rulesPullMaxDownloadBytes int64 = 32 << 20 // bytes read from the network
	rulesPullMaxTarEntries          = 10000    // every tar header, rule file or not
	rulesPullMaxTarBytes      int64 = 64 << 20 // the decompressed tar stream as a whole
	rulesPullMaxEntryBytes    int64 = 1 << 20  // one .rule.yaml or definitions file
)

// rulesPullTransport is the HTTP transport `beacon rules pull` uses; nil means the
// default. Tests set it to trust an httptest TLS server.
var rulesPullTransport http.RoundTripper

// checkRulesPullURL accepts https URLs, and plain http only for a loopback host so a
// local test server still works. Everything else is refused before any request.
func checkRulesPullURL(u *neturl.URL) error {
	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.Host == "" {
			return fmt.Errorf("url %q has no host", u.Redacted())
		}
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("url must use https:// (plain http:// is accepted only for a loopback host such as 127.0.0.1)")
	default:
		return fmt.Errorf("url must start with https://")
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// rulesPullCheckRedirect refuses a redirect that leaves https, and any redirect to a
// URL the command would not have accepted in the first place.
func rulesPullCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	if strings.EqualFold(via[0].URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
		return fmt.Errorf("refusing redirect from https to %s://%s", req.URL.Scheme, req.URL.Host)
	}
	if err := checkRulesPullURL(req.URL); err != nil {
		return fmt.Errorf("refusing redirect to %s: %w", req.URL.Redacted(), err)
	}
	return nil
}

// parseRulesPullSHA256 normalizes an optional --sha256 value: empty means no check,
// anything else must be 64 hex digits (an optional "sha256:" prefix is allowed).
func parseRulesPullSHA256(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.TrimPrefix(v, "sha256:")
	if v == "" {
		return "", nil
	}
	if len(v) != sha256.Size*2 {
		return "", fmt.Errorf("--sha256 must be %d hex digits", sha256.Size*2)
	}
	if _, err := hex.DecodeString(v); err != nil {
		return "", fmt.Errorf("--sha256 must be hex: %w", err)
	}
	return v, nil
}

func runRulesPull(cmd *cobra.Command, args []string) error {
	rawURL := args[0]
	wantSum, err := parseRulesPullSHA256(rulesOpts.sha256)
	if err != nil {
		return err
	}
	// Detect the pack format from the URL path only; query/fragment must not
	// influence the suffix match or the downloaded filename.
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if err := checkRulesPullURL(parsed); err != nil {
		return err
	}
	urlPath := parsed.Path
	isTarball := strings.HasSuffix(urlPath, ".tar.gz") || strings.HasSuffix(urlPath, ".tgz")
	if !isTarball && !strings.HasSuffix(urlPath, ".rule.yaml") {
		return fmt.Errorf("unsupported url: expected a .rule.yaml or .tar.gz/.tgz pack")
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Fetching %s (network)…\n", rawURL)

	client := &http.Client{Timeout: 60 * time.Second, Transport: rulesPullTransport, CheckRedirect: rulesPullCheckRedirect}
	resp, err := client.Get(parsed.String())
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: HTTP %d", rawURL, resp.StatusCode)
	}
	if resp.ContentLength > rulesPullMaxDownloadBytes {
		return fmt.Errorf("download is %d bytes, over the %d-byte limit for a rule pack", resp.ContentLength, rulesPullMaxDownloadBytes)
	}
	// Read the whole pack into memory (bounded) so it is verified before a single byte
	// is unpacked or installed.
	data, err := io.ReadAll(io.LimitReader(resp.Body, rulesPullMaxDownloadBytes+1))
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if int64(len(data)) > rulesPullMaxDownloadBytes {
		return fmt.Errorf("download exceeds the %d-byte limit for a rule pack", rulesPullMaxDownloadBytes)
	}
	sum := sha256.Sum256(data)
	gotSum := hex.EncodeToString(sum[:])
	if wantSum != "" {
		if subtle.ConstantTimeCompare([]byte(gotSum), []byte(wantSum)) != 1 {
			return fmt.Errorf("sha256 mismatch: downloaded %s, expected %s; nothing was installed", gotSum, wantSum)
		}
		fmt.Fprintf(out, "Verified sha256 %s\n", gotSum)
	} else {
		fmt.Fprintf(out, "Downloaded %d bytes, sha256 %s (pass --sha256 to verify)\n", len(data), gotSum)
	}

	tmp, err := os.MkdirTemp("", "beacon-rules-pull-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	var src string
	if isTarball {
		if err := extractRuleTarball(bytes.NewReader(data), tmp); err != nil {
			return err
		}
		src = tmp
	} else {
		if int64(len(data)) > rulesPullMaxEntryBytes {
			return fmt.Errorf("rule file is %d bytes, over the %d-byte limit for one rule", len(data), rulesPullMaxEntryBytes)
		}
		dest := filepath.Join(tmp, path.Base(urlPath))
		if err := os.WriteFile(dest, data, 0o600); err != nil {
			return err
		}
		src = dest
	}

	installed, skipped, err := detect.InstallFilesSkipping(rulesUserMode(), src, rulesOpts.force)
	if err != nil {
		return err
	}
	detect.WarnSkipped(cmd.ErrOrStderr(), skipped)
	printInstalled(out, installed)
	return nil
}

// errTarStreamTooLarge is returned by the bounded reader under the tar stream.
var errTarStreamTooLarge = errors.New("tarball exceeds the uncompressed size limit")

// boundedReader fails, rather than silently truncating, once more than n bytes are read.
type boundedReader struct {
	r io.Reader
	n int64
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.n <= 0 {
		// Probe for one more byte so a stream of exactly the limit still ends cleanly.
		var one [1]byte
		k, err := b.r.Read(one[:])
		if k > 0 {
			return 0, errTarStreamTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > b.n {
		p = p[:b.n]
	}
	k, err := b.r.Read(p)
	b.n -= int64(k)
	return k, err
}

func tarLimitError(err error) error {
	if errors.Is(err, errTarStreamTooLarge) {
		return fmt.Errorf("tarball exceeds the %d-byte uncompressed limit for a rule pack", rulesPullMaxTarBytes)
	}
	return nil
}

// extractRuleTarball extracts the *.rule.yaml entries of a gzipped tarball into dir, and the
// definitions entries (*.catalog.yaml, *.predicates.yaml) the rules are installed with. Entry
// paths keep their relative archive layout so two entries that share a base name (e.g.
// network/exfil.rule.yaml and secrets/exfil.rule.yaml) do not overwrite each other;
// path-traversal entries are rejected. InstallFiles walks dir recursively and keys the
// installed file on rule id, so preserving subdirectories here loses no rules. The
// number of entries, the size of each extracted entry and the decompressed size of the whole
// stream are capped, so a gzip bomb fails with an error instead of filling the disk.
func extractRuleTarball(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	cleanDir := filepath.Clean(dir)
	tr := tar.NewReader(&boundedReader{r: gz, n: rulesPullMaxTarBytes})
	count, entries := 0, 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if limitErr := tarLimitError(err); limitErr != nil {
			return limitErr
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		entries++
		if entries > rulesPullMaxTarEntries {
			return fmt.Errorf("tarball has more than %d entries", rulesPullMaxTarEntries)
		}
		isRule := strings.HasSuffix(hdr.Name, ".rule.yaml")
		if hdr.Typeflag != tar.TypeReg || !isRule && !threatrules.IsDefinitionFile(hdr.Name) || isAppleDoubleArchiveEntry(hdr.Name) {
			continue
		}
		// Reject any entry whose name contains a path-traversal element before it is
		// used in a filesystem path — the primary guard against archive path traversal
		// ("Zip Slip").
		if strings.Contains(hdr.Name, "..") {
			return fmt.Errorf("archive entry %q contains a path traversal element", hdr.Name)
		}
		if hdr.Size > rulesPullMaxEntryBytes {
			what := "rule"
			if !isRule {
				what = "definitions file"
			}
			return fmt.Errorf("archive entry %q is %d bytes, over the %d-byte limit for one %s", hdr.Name, hdr.Size, rulesPullMaxEntryBytes, what)
		}
		// Preserve the entry's relative directory structure (rejecting absolute paths),
		// then confirm the joined destination stays within cleanDir before any file
		// operation (defense in depth; the same `dest` value is both validated and
		// written). Keeping subdirectories prevents same-base-name entries from silently
		// overwriting each other.
		rel := filepath.Clean(filepath.FromSlash(hdr.Name))
		if filepath.IsAbs(rel) {
			return fmt.Errorf("archive entry %q is an absolute path", hdr.Name)
		}
		dest := filepath.Join(cleanDir, rel)
		if dest != cleanDir && !strings.HasPrefix(dest, cleanDir+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry %q resolves outside the destination directory", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := writeReaderToFile(io.LimitReader(tr, rulesPullMaxEntryBytes), dest); err != nil {
			if limitErr := tarLimitError(err); limitErr != nil {
				return limitErr
			}
			return err
		}
		if isRule {
			count++
		}
	}
	if count == 0 {
		return fmt.Errorf("tarball contained no .rule.yaml files")
	}
	return nil
}

func isAppleDoubleArchiveEntry(name string) bool {
	for _, part := range strings.Split(path.Clean(name), "/") {
		if part == "__MACOSX" || strings.HasPrefix(part, "._") {
			return true
		}
	}
	return false
}

func writeReaderToFile(r io.Reader, dest string) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return nil
}

// --- authoring commands (moved from the retired `beacon threatrules` group) ---

var rulesLintCmd = &cobra.Command{
	Use:   "lint [path]",
	Short: "Validate rule files and run their embedded conformance fixtures",
	Long: `Loads a rule file or a directory of *.rule.yaml files, validates each (structure
plus CEL compilation against the event schema), enforces the maturity gate, and runs every
embedded test fixture. Exits non-zero if anything fails. With no path, lints ./rules.

A directory is linted as a pack: its definitions (*.catalog.yaml and *.predicates.yaml, found
anywhere under it) are loaded first, a catalog's examples and a predicate's fixtures are run,
and the rules are compiled against them. A rule file linted on its own has no definitions, so
one that calls a predicate is linted through its pack's directory.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "rules"
		if len(args) == 1 {
			path = args[0]
		}
		return lintRulesPath(cmd, path)
	},
}

var rulesFieldsCmd = &cobra.Command{
	Use:          "fields",
	Short:        "Print the event fields a rule's CEL expression can match on",
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if rulesOpts.markdown {
			fmt.Fprint(cmd.OutOrStdout(), threatrules.RenderFieldsMarkdown())
			return nil
		}
		for _, f := range threatrules.EventFields() {
			fmt.Fprintf(cmd.OutOrStdout(), "e.%-44s %s\n", f.Path, f.Type)
		}
		return nil
	},
}

// loadRuleFiles loads a rule file or directory for lint. Rules that need a newer spec
// than this binary supports come back in skipped rather than failing the load.
func loadRuleFiles(path string) ([]*threatrules.Rule, []threatrules.SkippedRule, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.IsDir() {
		return threatrules.LoadDirSkipping(path)
	}
	rule, err := threatrules.LoadRule(path)
	if unsupported, ok := threatrules.AsUnsupportedSpec(err); ok {
		return nil, []threatrules.SkippedRule{{Path: path, Err: unsupported}}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return []*threatrules.Rule{rule}, nil, nil
}

func lintRulesPath(cmd *cobra.Command, path string) error {
	out := cmd.OutOrStdout()
	rules, skipped, err := loadRuleFiles(path)
	if err != nil {
		return fmt.Errorf("load rules: %w", err)
	}
	// The pack's definitions loaded with the rules above, or the load would have failed; they
	// are read again here for their own fixtures.
	var lib *threatrules.Library
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		if lib, _, err = threatrules.LoadLibraryDir(path); err != nil {
			return fmt.Errorf("load rules: %w", err)
		}
	}
	predicates := lib.Predicates()
	if len(rules) == 0 && len(skipped) == 0 && len(predicates) == 0 {
		return fmt.Errorf("no *.rule.yaml files found under %s", filepath.Clean(path))
	}
	// Lint cannot vouch for a rule it cannot evaluate, so a skip fails the run, but the
	// rest of the pack is still checked.
	for _, s := range skipped {
		fmt.Fprintf(out, "SKIP %s\n", s)
	}

	failures, fixtures := 0, 0
	results := lib.Check()
	for _, p := range predicates {
		// A definition's fixtures are its own; a catalog's predicate is held to the catalog's
		// examples, which are not counted among the fixtures.
		ran, failed, catalog := 0, false, false
		for _, res := range results {
			if res.RuleID != p.Name {
				continue
			}
			if res.Fixture == threatrules.CatalogExamples {
				catalog = true
			} else {
				ran++
				fixtures++
			}
			if !res.OK() {
				failed = true
				failures++
				fmt.Fprintf(out, "FAIL predicate %s\n", res.String())
			}
		}
		switch {
		case failed:
		case catalog:
			fmt.Fprintf(out, "ok   predicate %s (%s)\n", p.Signature, p.Source)
		default:
			fmt.Fprintf(out, "ok   predicate %s (%d fixtures)\n", p.Signature, ran)
		}
	}
	for _, rule := range rules {
		results, err := threatrules.CheckRule(rule)
		if err != nil {
			failures++
			fmt.Fprintf(out, "FAIL %s: %v\n", rule.ID, err)
			continue
		}
		ruleFailed := false
		for _, res := range results {
			fixtures++
			if !res.OK() {
				ruleFailed = true
				failures++
				fmt.Fprintf(out, "FAIL %s\n", res.String())
			}
		}
		// A rule that carries its own copy of what a catalog of its pack says will drift from it.
		for _, copied := range threatrules.CatalogCopies(rule) {
			ruleFailed = true
			failures++
			fmt.Fprintf(out, "FAIL %s: %s\n", rule.ID, copied)
		}
		if !ruleFailed {
			fmt.Fprintf(out, "ok   %s (%d fixtures)\n", rule.ID, len(results))
		}
	}
	counted := fmt.Sprintf("%d rule(s)", len(rules))
	if len(predicates) > 0 {
		counted += fmt.Sprintf(", %d predicate(s)", len(predicates))
	}
	if len(skipped) > 0 {
		fmt.Fprintf(out, "\n%s, %d fixture(s), %d failure(s), %d skipped (need a newer Beacon)\n", counted, fixtures, failures, len(skipped))
	} else {
		fmt.Fprintf(out, "\n%s, %d fixture(s), %d failure(s)\n", counted, fixtures, failures)
	}
	switch {
	case failures > 0:
		return fmt.Errorf("%d failure(s)", failures)
	case len(skipped) > 0:
		return fmt.Errorf("%d rule(s) need a newer Beacon (supports up to %s)", len(skipped), threatrules.SupportedSpec)
	}
	return nil
}

func init() {
	rulesFieldsCmd.Flags().BoolVar(&rulesOpts.markdown, "markdown", false, "Render as the FIELDS.md reference table")
	for _, c := range []*cobra.Command{rulesListCmd, rulesAddCmd, rulesRemoveCmd, rulesPullCmd} {
		c.Flags().BoolVar(&rulesOpts.userMode, "user", true, "Use per-user endpoint paths")
		c.Flags().BoolVar(&rulesOpts.systemMode, "system", false, "Use system endpoint paths")
	}
	for _, c := range []*cobra.Command{rulesAddCmd, rulesPullCmd} {
		c.Flags().BoolVar(&rulesOpts.force, "force", false, "Overwrite an existing rule with the same id")
	}
	rulesPullCmd.Flags().StringVar(&rulesOpts.sha256, "sha256", "", "Expected SHA-256 of the downloaded file (hex); nothing is installed on a mismatch")
	rulesCmd.AddCommand(rulesListCmd, rulesAddCmd, rulesRemoveCmd, rulesPullCmd, rulesLintCmd, rulesFieldsCmd)
	rootCmd.AddCommand(rulesCmd)
}
