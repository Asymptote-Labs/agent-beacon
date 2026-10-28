package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/agentskills"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/inventory"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/intel"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/version"
	"github.com/asymptote-labs/agent-beacon/pkg/asymptoteobserve/intelapi"
)

// Beacon Intel tools: check an MCP server or skill against the free public index at
// intel.beacon.sh before installing or running it. These are the local server's only tools
// that reach the network, and only when an agent calls one; each sends just what it names
// (intel_check_installed: the public identifiers internal/intel derives from inventory).

// intelInstalledMatches is how many matches intel_check_installed keeps per artifact.
const intelInstalledMatches = 3

func defaultIntelClient() (*intelapi.Client, error) {
	return intel.New("beacon-mcp/" + version.GetVersion() + " (+https://beacon.sh)")
}

func defaultInventory(includeProject bool) inventory.Result {
	opts := inventory.Options{IncludeContents: true, SkipProjectScope: !includeProject}
	if includeProject {
		if wd, err := os.Getwd(); err == nil {
			opts.WorkingDir = wd
		}
	}
	return inventory.Scan(opts)
}

func (s *Server) intelClient() (*intelapi.Client, error) {
	if s.newIntel != nil {
		return s.newIntel()
	}
	return defaultIntelClient()
}

// intelToolNames is what this server calls the tools intelapi defines for every Beacon server;
// Beacon Cloud MCP names the same tools beacon_intel_*.
var intelToolNames = intelapi.ToolNames{Lookup: "intel_lookup", GetArtifact: "intel_get_artifact", Check: "intel_check"}

func (s *Server) registerIntelTools() {
	// Each tool parses its arguments into the one request it makes. Arguments are checked before
	// the client is made, so a bad call says what is wrong with it even when BEACON_INTEL=0.
	type request func(context.Context, *intelapi.Client) ([]byte, error)
	parse := map[string]func(intelapi.Args) (request, error){
		intelToolNames.Lookup: func(a intelapi.Args) (request, error) {
			req, err := intelapi.ParseLookupArgs(a)
			return func(ctx context.Context, c *intelapi.Client) ([]byte, error) {
				_, raw, err := c.Lookup(ctx, req.Q, req.Kind, req.Limit)
				return raw, err
			}, err
		},
		intelToolNames.GetArtifact: func(a intelapi.Args) (request, error) {
			key, err := intelapi.ParseArtifactArgs(a)
			return func(ctx context.Context, c *intelapi.Client) ([]byte, error) {
				_, raw, err := c.Artifact(ctx, key)
				if intelapi.NotFound(err) {
					return nil, fmt.Errorf("no artifact in Beacon Intel has that key; find one with %s", intelToolNames.Lookup)
				}
				return raw, err
			}, err
		},
		intelToolNames.Check: func(a intelapi.Args) (request, error) {
			items, err := intelapi.ParseCheckArgs(a)
			return func(ctx context.Context, c *intelapi.Client) ([]byte, error) {
				_, raw, err := c.Check(ctx, items)
				return raw, err
			}, err
		},
	}
	for _, spec := range intelapi.Tools(intelToolNames) {
		parseArgs := parse[spec.Name]
		schema := objectSchema(spec.Properties, spec.Required)
		schema["additionalProperties"] = false
		s.register(Tool{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: schema,
			handler: func(ctx context.Context, args map[string]interface{}) (interface{}, error) {
				a, err := intelapi.ArgsFromMap(args)
				if err != nil {
					return nil, err
				}
				send, err := parseArgs(a)
				if err != nil {
					return nil, err
				}
				client, err := s.intelClient()
				if err != nil {
					return nil, err
				}
				raw, err := send(ctx, client)
				if err != nil {
					return nil, err
				}
				return json.RawMessage(raw), nil
			},
		})
	}
	s.register(Tool{
		Name: "intel_check_installed",
		Description: "Check the MCP servers and skills configured on this machine against Beacon Intel, the way a dependency audit checks a lockfile. " +
			"Beacon reads each runtime's own config (Claude Code, Cursor, Codex, and the rest Beacon supports) and sends only public identifiers: npm or PyPI packages named in a server's launch command, and skill names. " +
			"Paths, URLs, environment and other arguments never leave the machine; servers with no package are listed as skipped unless include_server_names is set. " +
			fmt.Sprintf("Use dry_run to see exactly what would be sent without sending it. Read a concerning result in full with %s.", intelToolNames.GetArtifact),
		InputSchema: objectSchema(map[string]interface{}{
			"include_server_names": map[string]interface{}{"type": "boolean", "description": "Also look up servers with no package in their command by their configured name. Off by default: a private server's name is the user's."},
			"include_skills":       map[string]interface{}{"type": "boolean", "description": "Check installed skills by name (default true)."},
			"include_project":      map[string]interface{}{"type": "boolean", "description": "Also read project-scoped config in the server's working directory (default true)."},
			"dry_run":              map[string]interface{}{"type": "boolean", "description": "List what would be sent, and send nothing."},
		}, nil),
		handler: s.checkInstalled,
	})
}

func boolArg(args map[string]interface{}, key string, fallback bool) bool {
	if value, ok := args[key].(bool); ok {
		return value
	}
	return fallback
}

type installedResult struct {
	Kind     string                  `json:"kind"`
	Q        string                  `json:"q"`
	Basis    string                  `json:"basis"`
	FoundIn  []intel.InstalledSource `json:"found_in"`
	Status   string                  `json:"status"`
	Risk     string                  `json:"risk,omitempty"`
	Guidance string                  `json:"guidance,omitempty"`
	// NameOnly marks a match made on a name alone: it may be a different artifact that
	// happens to share the name, so its risk is a lead to confirm, not a verdict on this one.
	NameOnly bool               `json:"name_only,omitempty"`
	Matches  []intelapi.Summary `json:"matches,omitempty"`
}

const nameOnlyGuidance = "Matched by name only: confirm the installed one comes from the matched repository before relying on this. "

type installedReport struct {
	DryRun  bool                 `json:"dry_run,omitempty"`
	Sent    []intelapi.CheckItem `json:"sent"`
	Results []installedResult    `json:"results,omitempty"`
	Counts  map[string]int       `json:"counts,omitempty"`
	Skipped []intel.Skipped      `json:"skipped"`
	Note    string               `json:"note"`
}

func (s *Server) checkInstalled(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	includeProject := boolArg(args, "include_project", true)
	var inv inventory.Result
	if s.inventory != nil {
		inv = s.inventory(includeProject)
	} else {
		inv = defaultInventory(includeProject)
	}
	if !boolArg(args, "include_skills", true) {
		inv.Skills = nil
	}
	items, skipped := intel.FromInventory(inv, intel.FromInventoryOptions{
		IncludeServerNames: boolArg(args, "include_server_names", false),
		BeaconSkills:       agentskills.Names(),
	})
	report := installedReport{Sent: make([]intelapi.CheckItem, len(items)), Skipped: skipped}
	if report.Skipped == nil {
		report.Skipped = []intel.Skipped{}
	}
	for i, item := range items {
		report.Sent[i] = intelapi.CheckItem{Q: item.Q, Kind: item.Kind}
	}
	if boolArg(args, "dry_run", false) {
		report.DryRun = true
		report.Note = "Nothing was sent. These lookups would go to Beacon Intel; skipped entries would not."
		return report, nil
	}
	if len(items) == 0 {
		report.Note = "No MCP server or skill on this machine has a public identifier to check; see skipped."
		return report, nil
	}
	client, err := s.intelClient()
	if err != nil {
		return nil, err
	}
	report.Counts = map[string]int{}
	for start := 0; start < len(items); start += intelapi.MaxCheck {
		end := min(start+intelapi.MaxCheck, len(items))
		res, _, err := client.Check(ctx, report.Sent[start:end])
		if err != nil {
			return nil, err
		}
		if len(res.Results) != end-start {
			return nil, fmt.Errorf("Beacon Intel answered %d results for %d artifacts", len(res.Results), end-start)
		}
		for i, outcome := range res.Results {
			item := items[start+i]
			matches := outcome.Matches
			if len(matches) > intelInstalledMatches {
				matches = matches[:intelInstalledMatches]
			}
			result := installedResult{
				Kind: item.Kind, Q: item.Q, Basis: item.Basis, FoundIn: item.Sources,
				Status: outcome.Status, Risk: outcome.Risk, Guidance: outcome.Guidance, Matches: matches,
			}
			if outcome.Status == "matched" && (item.Basis == intel.BasisSkillName || item.Basis == intel.BasisServerName) {
				result.NameOnly = true
				result.Guidance = nameOnlyGuidance + result.Guidance
			}
			report.Results = append(report.Results, result)
			key := outcome.Status
			if outcome.Status == "matched" && outcome.Risk != "" {
				key = outcome.Risk
			}
			report.Counts[key]++
		}
	}
	report.Note = "not_found means never checked, not safe. ambiguous claims no risk: check a candidate with intel_get_artifact. " + intelapi.UntrustedNote
	return report, nil
}

// intelTools lists the Beacon Intel tools, for doctor and tests.
func intelTools() []string {
	return []string{intelToolNames.Lookup, intelToolNames.GetArtifact, intelToolNames.Check, "intel_check_installed"}
}
