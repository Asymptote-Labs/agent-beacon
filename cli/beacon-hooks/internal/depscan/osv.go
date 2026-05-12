package depscan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/asymptote-labs/agent-beacon/cli/beacon-hooks/internal/logging"
)

const (
	osvAPIURL       = "https://api.osv.dev/v1/query"
	osvQueryTimeout = 10 * time.Second
)

// OSVVulnerability represents a vulnerability from OSV.dev.
type OSVVulnerability struct {
	ID           string
	Summary      string
	Aliases      []string
	Severity     string
	FixedVersion string
}

// osvQueryRequest is the request body for OSV.dev /v1/query.
type osvQueryRequest struct {
	Version string     `json:"version"`
	Package osvPackage `json:"package"`
}

type osvPackage struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

// osvQueryResponse is the response from OSV.dev /v1/query.
type osvQueryResponse struct {
	Vulns []osvVuln `json:"vulns"`
}

type osvVuln struct {
	ID               string               `json:"id"`
	Summary          string               `json:"summary"`
	Aliases          []string             `json:"aliases"`
	DatabaseSpecific *osvDatabaseSpecific `json:"database_specific"`
	Affected         []osvAffected        `json:"affected"`
}

type osvDatabaseSpecific struct {
	Severity string `json:"severity"`
}

type osvAffected struct {
	Ranges []osvRange `json:"ranges"`
}

type osvRange struct {
	Events []osvEvent `json:"events"`
}

type osvEvent struct {
	Introduced string `json:"introduced,omitempty"`
	Fixed      string `json:"fixed,omitempty"`
}

// QueryPackage queries OSV.dev for vulnerabilities affecting a single package.
func QueryPackage(ctx context.Context, pkg DetectedPackage) ([]OSVVulnerability, error) {
	reqBody := osvQueryRequest{
		Version: pkg.Version,
		Package: osvPackage{
			Name:      pkg.Name,
			Ecosystem: pkg.Ecosystem,
		},
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, osvAPIURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query osv.dev: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("osv.dev returned status %d", resp.StatusCode)
	}

	var osvResp osvQueryResponse
	if err := json.NewDecoder(resp.Body).Decode(&osvResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	var vulns []OSVVulnerability
	for _, v := range osvResp.Vulns {
		vuln := convertVuln(v)
		// Only report HIGH and CRITICAL severity vulnerabilities
		sev := strings.ToUpper(vuln.Severity)
		if sev != "HIGH" && sev != "CRITICAL" {
			continue
		}
		vulns = append(vulns, vuln)
	}
	return vulns, nil
}

// QueryPackages queries OSV.dev for all packages in parallel with a shared timeout.
func QueryPackages(pkgs []DetectedPackage, logger *logging.Logger) map[string][]OSVVulnerability {
	ctx, cancel := context.WithTimeout(context.Background(), osvQueryTimeout)
	defer cancel()

	logger.Info("OSV query started", "package_count", len(pkgs))

	type result struct {
		key   string
		vulns []OSVVulnerability
		err   error
		pkg   DetectedPackage
	}

	results := make(chan result, len(pkgs))
	var wg sync.WaitGroup

	for _, pkg := range pkgs {
		wg.Add(1)
		go func(p DetectedPackage) {
			defer wg.Done()
			vulns, err := QueryPackage(ctx, p)
			results <- result{
				key:   p.Name + "@" + p.Version,
				vulns: vulns,
				err:   err,
				pkg:   p,
			}
		}(pkg)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	findings := make(map[string][]OSVVulnerability)
	for r := range results {
		if r.err != nil {
			if ctx.Err() != nil {
				logger.Warn("OSV query timeout", "package", r.pkg.Name, "version", r.pkg.Version)
			} else {
				logger.Warn("OSV query failed", "package", r.pkg.Name, "version", r.pkg.Version, "error", r.err.Error())
			}
			continue
		}
		logger.Debug("OSV query per-package result", "package", r.pkg.Name, "version", r.pkg.Version, "vuln_count", len(r.vulns))
		if len(r.vulns) > 0 {
			findings[r.key] = r.vulns
		}
	}

	return findings
}

// convertVuln converts an OSV API vulnerability to our internal representation.
func convertVuln(v osvVuln) OSVVulnerability {
	vuln := OSVVulnerability{
		ID:      v.ID,
		Summary: v.Summary,
		Aliases: v.Aliases,
	}

	// Extract severity from database_specific (e.g. GHSA advisories)
	if v.DatabaseSpecific != nil && v.DatabaseSpecific.Severity != "" {
		vuln.Severity = v.DatabaseSpecific.Severity
	}

	// Extract highest fixed version from affected ranges
	for _, affected := range v.Affected {
		for _, r := range affected.Ranges {
			for _, event := range r.Events {
				if event.Fixed != "" && compareVersions(event.Fixed, vuln.FixedVersion) > 0 {
					vuln.FixedVersion = event.Fixed
				}
			}
		}
	}

	return vuln
}
