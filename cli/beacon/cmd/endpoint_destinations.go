package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/cloudwatch"
	endpointconfig "github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/config"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/datadog"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/elastic"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/falcon"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/gcs"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/rapid7"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/s3"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sentinel"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/sumo"
	"github.com/asymptote-labs/agent-beacon/cli/beacon/internal/endpoint/wazuh"
)

// siemDestination describes one local/customer-managed forwarding destination
// and the uniform `print-config` / `install-pack` / `validate` verbs it exposes
// under `beacon endpoint <name>`. It is the single source of truth for the
// destination command tree (built by buildDestinationCommands) and for the
// synthetic validation-event metadata consumed by syntheticEvent.
//
// Adding a destination means adding a table row plus its content-pack package;
// non-uniform verbs (such as elastic's local-stack up/down) attach through the
// extra hook.
type siemDestination struct {
	// name is the canonical destination id. It is the command group's Use name
	// and the destination passed to writeValidationEvent / syntheticEvent.
	name  string
	short string

	printConfig *destPrintConfig
	installPack *destInstallPack
	validate    *destValidate

	// extra attaches non-uniform subcommands to the group (e.g. elastic up/down).
	extra func(group *cobra.Command)
}

type destPrintConfig struct {
	short string
	// render returns the snippet to print for `print-config`. Destinations whose
	// underlying helper cannot fail wrap it to return a nil error.
	render func(cfg endpointconfig.Config) (string, error)
}

type destInstallPack struct {
	short string
	// defaultOutputDir is used when --output is omitted. An empty value makes
	// --output required.
	defaultOutputDir string
	// successLabel is printed verbatim followed by the resolved output directory.
	successLabel   string
	outputFlagHelp string
	install        func(outputDir, logPath string) error
}

type destValidate struct {
	short string
	// mode and message populate the synthetic validation event for this
	// destination (see syntheticEvent).
	mode    string
	message string
	// print emits the post-write guidance lines shown after the validation event
	// is written.
	print func(cfg endpointconfig.Config)
}

// siemDestinations is the registry of forwarding destinations. Order does not
// affect help or generated docs (cobra sorts commands), but is kept stable for
// readability.
var siemDestinations = []siemDestination{
	{
		name:  "falcon",
		short: "Manage CrowdStrike Falcon forwarding content",
		printConfig: &destPrintConfig{
			short:  "Print a Vector config for CrowdStrike Falcon forwarding",
			render: func(cfg endpointconfig.Config) (string, error) { return falcon.ConfigSnippet(cfg.LogPath) },
		},
		installPack: &destInstallPack{
			short:            "Write CrowdStrike Falcon Vector forwarding content to a directory",
			defaultOutputDir: falcon.DefaultOutputDir,
			successLabel:     "CrowdStrike Falcon content pack written to ",
			outputFlagHelp:   "Output directory for CrowdStrike Falcon content pack",
			install:          falcon.InstallPack,
		},
		validate: &destValidate{
			short:   "Write and describe a CrowdStrike Falcon validation event",
			mode:    "crowdstrike_hec",
			message: "Beacon endpoint Falcon validation event",
			print: func(cfg endpointconfig.Config) {
				fmt.Println("Expected Falcon fields: vendor=beacon product=endpoint-agent destination.type=falcon destination.mode=crowdstrike_hec")
				fmt.Println(`Expected validation query: source = "beacon-endpoint-agent" "Beacon endpoint Falcon validation event"`)
				fmt.Println(`Hook-only query: source = "beacon-endpoint-agent" "hook-only unique marker"`)
			},
		},
	},
	{
		name:  "wazuh",
		short: "Manage Wazuh integration content",
		printConfig: &destPrintConfig{
			short: "Print a Wazuh localfile snippet",
			render: func(cfg endpointconfig.Config) (string, error) {
				return wazuh.LocalfileSnippet(cfg.LogPath), nil
			},
		},
		installPack: &destInstallPack{
			short:          "Write Wazuh rules and config snippets to a directory",
			successLabel:   "Wazuh content pack written to ",
			outputFlagHelp: "Output directory for Wazuh content pack",
			install:        wazuh.InstallPack,
		},
		validate: &destValidate{
			short:   "Write and describe a Wazuh validation event",
			mode:    "localfile",
			message: "Beacon endpoint Wazuh validation event",
			print: func(cfg endpointconfig.Config) {
				fmt.Println("Expected Wazuh fields: vendor=beacon product=endpoint-agent event.kind=agent_runtime")
				fmt.Println("Wazuh localfile snippet:")
				fmt.Print(wazuh.LocalfileSnippet(cfg.LogPath))
				fmt.Println("Expected base rule: 100500")
			},
		},
	},
	{
		name:  "elastic",
		short: "Manage Elasticsearch integration content",
		printConfig: &destPrintConfig{
			short:  "Print a Filebeat input for Beacon endpoint events",
			render: func(cfg endpointconfig.Config) (string, error) { return elastic.InputSnippet(cfg.LogPath) },
		},
		installPack: &destInstallPack{
			short:            "Write Elasticsearch templates, pipeline, and Filebeat content to a directory",
			defaultOutputDir: elastic.DefaultOutputDir,
			successLabel:     "Elasticsearch content pack written to ",
			outputFlagHelp:   "Output directory for Elasticsearch content pack",
			install:          elastic.InstallPack,
		},
		// elastic has no validate verb; it adds the local-stack up/down commands.
		extra: func(group *cobra.Command) {
			up := &cobra.Command{
				Use:          "up",
				Short:        "Start a local Elasticsearch, Kibana, and Filebeat stack",
				SilenceUsage: true,
				RunE:         func(cmd *cobra.Command, args []string) error { return runEndpointElasticUp(cmd.Context()) },
			}
			down := &cobra.Command{
				Use:          "down",
				Short:        "Stop the local Elasticsearch stack",
				SilenceUsage: true,
				RunE:         func(cmd *cobra.Command, args []string) error { return runEndpointElasticDown(cmd.Context()) },
			}
			addEndpointPathFlags(up)
			addEndpointPathFlags(down)
			up.Flags().StringVar(&endpointOpts.elasticPackDir, "pack-dir", elastic.DefaultOutputDir, "Elasticsearch pack directory")
			down.Flags().StringVar(&endpointOpts.elasticPackDir, "pack-dir", elastic.DefaultOutputDir, "Elasticsearch pack directory")
			group.AddCommand(up)
			group.AddCommand(down)
		},
	},
	{
		name:  "datadog",
		short: "Manage Datadog integration content",
		printConfig: &destPrintConfig{
			short:  "Print a Datadog Agent custom log config for Beacon endpoint events",
			render: func(cfg endpointconfig.Config) (string, error) { return datadog.ConfigSnippet(cfg.LogPath) },
		},
		installPack: &destInstallPack{
			short:            "Write Datadog Agent custom log integration content to a directory",
			defaultOutputDir: datadog.DefaultOutputDir,
			successLabel:     "Datadog content pack written to ",
			outputFlagHelp:   "Output directory for Datadog content pack",
			install:          datadog.InstallPack,
		},
		validate: &destValidate{
			short:   "Write and describe a Datadog validation event",
			mode:    "agent_file",
			message: "Beacon endpoint datadog validation event",
			print: func(cfg endpointconfig.Config) {
				fmt.Println("Expected Datadog fields: service=beacon-endpoint-agent vendor=beacon product=endpoint-agent")
				fmt.Println(`Expected validation query: service:beacon-endpoint-agent "Beacon endpoint datadog validation event"`)
			},
		},
	},
	{
		name:  "sumo",
		short: "Manage Sumo Logic integration content",
		printConfig: &destPrintConfig{
			short:  "Print a Sumo HTTP Source smoke-test uploader for Beacon endpoint events",
			render: func(cfg endpointconfig.Config) (string, error) { return sumo.UploadSmokeTest(cfg.LogPath) },
		},
		installPack: &destInstallPack{
			short:            "Write Sumo Logic HTTP Source forwarding content to a directory",
			defaultOutputDir: sumo.DefaultOutputDir,
			successLabel:     "Sumo Logic content pack written to ",
			outputFlagHelp:   "Output directory for Sumo Logic content pack",
			install:          sumo.InstallPack,
		},
		validate: &destValidate{
			short:   "Write and describe a Sumo Logic validation event",
			mode:    "http_source_jsonl",
			message: "Beacon endpoint Sumo validation event",
			print: func(cfg endpointconfig.Config) {
				fmt.Println("Expected Sumo fields: _sourceCategory=security/agentbeacon product=agentbeacon telemetry=ai_agent")
				fmt.Println(`Expected validation query: _sourceCategory=security/agentbeacon "Beacon endpoint Sumo validation event"`)
			},
		},
	},
	{
		name:  "rapid7",
		short: "Manage Rapid7 InsightIDR integration content",
		printConfig: &destPrintConfig{
			short:  "Print a Rapid7 Custom Logs webhook smoke-test uploader for Beacon endpoint events",
			render: func(cfg endpointconfig.Config) (string, error) { return rapid7.UploadSmokeTest(cfg.LogPath) },
		},
		installPack: &destInstallPack{
			short:            "Write Rapid7 InsightIDR Custom Logs forwarding content to a directory",
			defaultOutputDir: rapid7.DefaultOutputDir,
			successLabel:     "Rapid7 InsightIDR content pack written to ",
			outputFlagHelp:   "Output directory for Rapid7 InsightIDR content pack",
			install:          rapid7.InstallPack,
		},
		validate: &destValidate{
			short:   "Write and describe a Rapid7 InsightIDR validation event",
			mode:    "custom_logs_webhook_ndjson",
			message: "Beacon endpoint Rapid7 validation event",
			print: func(cfg endpointconfig.Config) {
				fmt.Println("Expected Rapid7 fields: vendor=beacon product=endpoint-agent destination.type=rapid7")
				fmt.Println(`Expected validation query: "Beacon endpoint Rapid7 validation event"`)
			},
		},
	},
	{
		name:  "s3",
		short: "Manage AWS S3 forwarding content",
		printConfig: &destPrintConfig{
			short:  "Print an AWS CLI S3 smoke-test uploader for Beacon endpoint events",
			render: func(cfg endpointconfig.Config) (string, error) { return s3.UploadSmokeTest(cfg.LogPath) },
		},
		installPack: &destInstallPack{
			short:            "Write AWS S3 forwarding content to a directory",
			defaultOutputDir: s3.DefaultOutputDir,
			successLabel:     "AWS S3 content pack written to ",
			outputFlagHelp:   "Output directory for AWS S3 content pack",
			install:          s3.InstallPack,
		},
		validate: &destValidate{
			short:   "Write and describe an AWS S3 validation event",
			mode:    "aws_s3_jsonl",
			message: "Beacon endpoint S3 validation event",
			print: func(cfg endpointconfig.Config) {
				fmt.Println("Expected S3 fields: vendor=beacon product=endpoint-agent destination.type=s3 destination.mode=aws_s3_jsonl")
				fmt.Println(`Confirm delivery with AWS CLI: aws s3 ls "s3://${BEACON_S3_BUCKET}/${BEACON_S3_PREFIX}/" --recursive`)
				fmt.Println(`Inspect an object with AWS CLI: aws s3 cp "s3://${BEACON_S3_BUCKET}/${BEACON_S3_PREFIX}/date=<date>/<object>.jsonl.gz" - | gzip -dc | grep "Beacon endpoint S3 validation event"`)
			},
		},
	},
	{
		name:  "cloudwatch",
		short: "Manage AWS CloudWatch Logs forwarding content",
		printConfig: &destPrintConfig{
			short:  "Print a Vector config for AWS CloudWatch Logs forwarding",
			render: func(cfg endpointconfig.Config) (string, error) { return cloudwatch.ConfigSnippet(cfg.LogPath) },
		},
		installPack: &destInstallPack{
			short:            "Write AWS CloudWatch Logs forwarding content to a directory",
			defaultOutputDir: cloudwatch.DefaultOutputDir,
			successLabel:     "AWS CloudWatch Logs content pack written to ",
			outputFlagHelp:   "Output directory for AWS CloudWatch Logs content pack",
			install:          cloudwatch.InstallPack,
		},
		validate: &destValidate{
			short:   "Write and describe an AWS CloudWatch Logs validation event",
			mode:    "aws_cloudwatch_logs",
			message: "Beacon endpoint AWS CloudWatch Logs validation event",
			print: func(cfg endpointconfig.Config) {
				fmt.Println("Expected AWS CloudWatch Logs fields: vendor=beacon product=endpoint-agent destination.type=cloudwatch destination.mode=aws_cloudwatch_logs")
				fmt.Println(`Confirm delivery with AWS CLI: aws logs filter-log-events --log-group-name "$BEACON_CLOUDWATCH_LOG_GROUP" --filter-pattern '"Beacon endpoint AWS CloudWatch Logs validation event"' --region "$AWS_REGION"`)
				fmt.Println(`CloudWatch Logs Insights query: fields @timestamp, vendor, product, destination.type, destination.mode, message | filter message like /Beacon endpoint AWS CloudWatch Logs validation event/ | sort @timestamp desc | limit 20`)
			},
		},
	},
	{
		name:  "gcs",
		short: "Manage Google Cloud Storage forwarding content",
		printConfig: &destPrintConfig{
			short:  "Print a Google Cloud Storage smoke-test uploader for Beacon endpoint events",
			render: func(cfg endpointconfig.Config) (string, error) { return gcs.UploadSmokeTest(cfg.LogPath) },
		},
		installPack: &destInstallPack{
			short:            "Write Google Cloud Storage forwarding content to a directory",
			defaultOutputDir: gcs.DefaultOutputDir,
			successLabel:     "Google Cloud Storage content pack written to ",
			outputFlagHelp:   "Output directory for Google Cloud Storage content pack",
			install:          gcs.InstallPack,
		},
		validate: &destValidate{
			short:   "Write and describe a Google Cloud Storage validation event",
			mode:    "google_cloud_storage_jsonl",
			message: "Beacon endpoint GCS validation event",
			print: func(cfg endpointconfig.Config) {
				fmt.Println("Expected GCS fields: vendor=beacon product=endpoint-agent destination.type=gcs destination.mode=google_cloud_storage_jsonl")
				fmt.Println(`Confirm delivery with Google Cloud CLI: gcloud storage ls "gs://${BEACON_GCS_BUCKET}/${BEACON_GCS_PREFIX}/**"`)
				fmt.Println(`Inspect an object with Google Cloud CLI: gcloud storage cat "gs://${BEACON_GCS_BUCKET}/${BEACON_GCS_PREFIX}/date=<date>/<object>.jsonl.gz" | gzip -dc | grep "Beacon endpoint GCS validation event"`)
			},
		},
	},
	{
		name:  "sentinel",
		short: "Manage Microsoft Sentinel integration content",
		printConfig: &destPrintConfig{
			short: "Print a Sentinel DCR transform for Beacon endpoint events",
			render: func(cfg endpointconfig.Config) (string, error) {
				return fmt.Sprintf("# Azure Monitor Agent file pattern: %s\n", cfg.LogPath) + sentinel.DCRTransform(), nil
			},
		},
		installPack: &destInstallPack{
			short:            "Write Microsoft Sentinel forwarding content to a directory",
			defaultOutputDir: sentinel.DefaultOutputDir,
			successLabel:     "Microsoft Sentinel content pack written to ",
			outputFlagHelp:   "Output directory for Microsoft Sentinel content pack",
			install:          sentinel.InstallPack,
		},
		validate: &destValidate{
			short:   "Write and describe a Microsoft Sentinel validation event",
			mode:    "azure_monitor_agent_custom_json_logs",
			message: "Beacon endpoint Sentinel validation event",
			print: func(cfg endpointconfig.Config) {
				fmt.Println("Expected Sentinel table: BeaconRuntime_CL")
				fmt.Println(`Expected validation query: BeaconRuntime_CL | where Message has "Beacon endpoint Sentinel validation event"`)
			},
		},
	},
}

// buildDestinationCommands returns the `endpoint <name>` group command for every
// registered forwarding destination.
func buildDestinationCommands() []*cobra.Command {
	groups := make([]*cobra.Command, 0, len(siemDestinations))
	for _, d := range siemDestinations {
		groups = append(groups, buildDestinationGroup(d))
	}
	return groups
}

func buildDestinationGroup(d siemDestination) *cobra.Command {
	group := &cobra.Command{Use: d.name, Short: d.short}

	if pc := d.printConfig; pc != nil {
		cmd := &cobra.Command{
			Use:          "print-config",
			Short:        pc.short,
			SilenceUsage: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg := loadOrDefaultConfig()
				out, err := pc.render(cfg)
				if err != nil {
					return err
				}
				fmt.Print(out)
				return nil
			},
		}
		addEndpointPathFlags(cmd)
		group.AddCommand(cmd)
	}

	if ip := d.installPack; ip != nil {
		cmd := &cobra.Command{
			Use:          "install-pack",
			Short:        ip.short,
			SilenceUsage: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg := loadOrDefaultConfig()
				outputDir := endpointOpts.outputDir
				if outputDir == "" {
					if ip.defaultOutputDir == "" {
						return fmt.Errorf("--output is required")
					}
					outputDir = ip.defaultOutputDir
				}
				if err := ip.install(outputDir, cfg.LogPath); err != nil {
					return err
				}
				fmt.Printf("%s%s\n", ip.successLabel, outputDir)
				return nil
			},
		}
		addEndpointPathFlags(cmd)
		cmd.Flags().StringVar(&endpointOpts.outputDir, "output", "", ip.outputFlagHelp)
		group.AddCommand(cmd)
	}

	if v := d.validate; v != nil {
		name := d.name
		cmd := &cobra.Command{
			Use:          "validate",
			Short:        v.short,
			SilenceUsage: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				cfg := loadOrDefaultConfig()
				path, err := writeValidationEvent(cfg, name)
				if err != nil {
					return err
				}
				fmt.Printf("Validation event written to %s\n", path)
				v.print(cfg)
				return nil
			},
		}
		addEndpointPathFlags(cmd)
		group.AddCommand(cmd)
	}

	if d.extra != nil {
		d.extra(group)
	}

	return group
}

// addEndpointPathFlags attaches the standard per-user/system/log-path flags
// shared by every destination subcommand.
func addEndpointPathFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&endpointOpts.userMode, "user", true, "Use per-user endpoint paths")
	cmd.Flags().BoolVar(&endpointOpts.systemMode, "system", false, "Use system endpoint paths and launch daemon")
	cmd.Flags().StringVar(&endpointOpts.logPath, "log-path", "", "Runtime JSONL log path")
}

// destinationValidationMeta returns the synthetic validation event mode and
// message for a destination, if it is a registered forwarding destination with
// a validate verb.
func destinationValidationMeta(name string) (mode, message string, ok bool) {
	for _, d := range siemDestinations {
		if d.validate != nil && d.name == name {
			return d.validate.mode, d.validate.message, true
		}
	}
	return "", "", false
}
