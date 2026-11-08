package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/config"
	"github.com/SteerSpec/strspc-sync/internal/conflict"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	sslog "github.com/SteerSpec/strspc-sync/internal/log"
	"github.com/SteerSpec/strspc-sync/internal/monitor"
	"github.com/SteerSpec/strspc-sync/internal/state"
	"github.com/SteerSpec/strspc-sync/internal/sync"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, out, errOut io.Writer) int {
	if len(args) < 1 {
		printUsageTo(errOut)
		return 1
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "sync":
		if err := runSync(rest, out, errOut); err != nil {
			_, _ = fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	case "monitor":
		if err := runMonitor(rest, out, errOut); err != nil {
			_, _ = fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	case "conflict":
		if err := runConflict(rest, out, errOut); err != nil {
			_, _ = fmt.Fprintln(errOut, err)
			return 1
		}
		return 0
	case "version":
		_, _ = fmt.Fprintf(out, "strspc %s (commit: %s)\n", version, commit)
		return 0
	case "help", "--help", "-h":
		printUsageTo(errOut)
		return 0
	default:
		_, _ = fmt.Fprintf(errOut, "unknown command: %s\n", cmd)
		printUsageTo(errOut)
		return 1
	}
}

func printUsageTo(w io.Writer) {
	_, _ = fmt.Fprintf(w, `Usage: strspc <command> [flags]

Commands:
  sync       Synchronize templates to target repositories
  monitor    Check target repositories for drift
  conflict   Detect conflicts across target repositories
  version    Print version information
  help       Show this help

Common Flags:
  --config <path>           Config file path (default: steerspec-sync.yml)
  --target-filter <glob>    Filter target repositories
  --timeout <duration>      Overall operation timeout (default: 10m)
  --log-level <level>       Log level: debug|info|warn|error (default: info)
  --log-format <format>     Log format: auto|json|text (default: auto)

Sync Flags:
  --dry-run                 Preview changes without creating PRs
  --template-filter <glob>  Filter templates
  --force                   Force sync even if up to date

Conflict Flags:
  --tiers <list>            Detection tiers to run (e.g. "1,2"; tier 3 is experimental, not implemented yet)
`)
}

type commonFlags struct {
	configPath   string
	targetFilter string
	timeout      time.Duration
	logLevel     string
	logFormat    string
}

func parseCommonFlags(args []string) (commonFlags, []string) {
	f := commonFlags{
		configPath: "steerspec-sync.yml",
		timeout:    10 * time.Minute,
		logLevel:   "info",
		logFormat:  "auto",
	}
	var remaining []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config":
			if i+1 < len(args) {
				f.configPath = args[i+1]
				i++
			}
		case "--target-filter":
			if i+1 < len(args) {
				f.targetFilter = args[i+1]
				i++
			}
		case "--timeout":
			if i+1 < len(args) {
				d, err := time.ParseDuration(args[i+1])
				if err == nil {
					f.timeout = d
				}
				i++
			}
		case "--log-level":
			if i+1 < len(args) {
				f.logLevel = args[i+1]
				i++
			}
		case "--log-format":
			if i+1 < len(args) {
				f.logFormat = args[i+1]
				i++
			}
		default:
			remaining = append(remaining, args[i])
		}
	}
	return f, remaining
}

// applyLogFlags parses the log-related fields on commonFlags and initializes
// the package-level logger to write to errOut (the same writer the run*
// functions use for diagnostics). Returns an error if either flag is malformed.
func applyLogFlags(f commonFlags, errOut io.Writer) error {
	level, err := sslog.ParseLevel(f.logLevel)
	if err != nil {
		return err
	}
	format, err := sslog.ParseFormat(f.logFormat)
	if err != nil {
		return err
	}
	sslog.InitTo(errOut, level, format)
	return nil
}

func loadConfig(path string) (*config.SyncConfig, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, fmt.Errorf("error loading config: %w", err)
	}
	return cfg, nil
}

func newGHClient(cfg *config.SyncConfig) (*gh.Client, error) {
	if cfg.Auth.Method == "github-token" && cfg.Auth.Token == "" {
		cfg.Auth.Token = os.Getenv("GITHUB_TOKEN")
	}
	if cfg.Auth.Method == "github-app" {
		if cfg.Auth.AppID == "" {
			cfg.Auth.AppID = os.Getenv("STEERSPEC_APP_ID")
		}
		if cfg.Auth.PrivateKey == "" {
			cfg.Auth.PrivateKey = os.Getenv("STEERSPEC_PRIVATE_KEY")
		}
		if cfg.Auth.InstallationID == "" {
			cfg.Auth.InstallationID = os.Getenv("STEERSPEC_INSTALLATION_ID")
		}
	}

	client, err := gh.NewClient(gh.AuthConfig{
		Method:         cfg.Auth.Method,
		AppID:          cfg.Auth.AppID,
		PrivateKey:     cfg.Auth.PrivateKey,
		InstallationID: cfg.Auth.InstallationID,
		Org:            orgFromTargets(cfg),
		Token:          cfg.Auth.Token,
	})
	if err != nil {
		return nil, fmt.Errorf("error creating GitHub client: %w", err)
	}
	return client, nil
}

// orgFromTargets extracts the org name from the first include pattern (e.g. "acme-corp/*" → "acme-corp").
func orgFromTargets(cfg *config.SyncConfig) string {
	for _, pattern := range cfg.Targets.Include {
		if i := strings.Index(pattern, "/"); i > 0 {
			return pattern[:i]
		}
	}
	return ""
}

func setOutput(key, value string) {
	outputFile := os.Getenv("GITHUB_OUTPUT")
	if outputFile == "" {
		return
	}
	f, err := os.OpenFile(outputFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()                       //nolint:errcheck // best-effort: output file write errors are non-fatal
	fmt.Fprintf(f, "%s=%s\n", key, value) //nolint:errcheck // best-effort: output file write errors are non-fatal
}

func runSync(args []string, out, errOut io.Writer) error {
	common, remaining := parseCommonFlags(args)
	if err := applyLogFlags(common, errOut); err != nil {
		return err
	}
	dryRun := false
	templateFilter := ""
	force := false

	for i := 0; i < len(remaining); i++ {
		switch remaining[i] {
		case "--dry-run":
			dryRun = true
		case "--template-filter":
			if i+1 < len(remaining) {
				templateFilter = remaining[i+1]
				i++
			}
		case "--force":
			force = true
		}
	}

	cfg, err := loadConfig(common.configPath)
	if err != nil {
		return err
	}
	client, err := newGHClient(cfg)
	if err != nil {
		return err
	}

	centralOwner, centralRepo, err := detectCentralRepo(cfg, errOut)
	if err != nil {
		return fmt.Errorf("resolving central repo: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), common.timeout)
	defer cancel()

	syncer := sync.New(cfg, client, centralOwner, centralRepo)
	result, err := syncer.Run(ctx, sync.Options{
		ConfigPath:     common.configPath,
		DryRun:         dryRun,
		TargetFilter:   common.targetFilter,
		TemplateFilter: templateFilter,
		Force:          force,
		Trigger:        detectTrigger(),
	})
	if err != nil {
		return fmt.Errorf("sync error: %w", err)
	}

	printJSON(result, out)

	setOutput("prs-created", strconv.Itoa(result.PRsCreated))
	setOutput("prs-updated", strconv.Itoa(result.PRsUpdated))
	setOutput("repos-skipped", strconv.Itoa(result.ReposSkipped))
	setOutput("errors", strconv.Itoa(result.Errors))
	summaryJSON, _ := json.Marshal(result)
	setOutput("summary", string(summaryJSON))

	if result.Errors > 0 {
		return fmt.Errorf("sync completed with %d error(s)", result.Errors)
	}
	return nil
}

func runMonitor(args []string, out, errOut io.Writer) error {
	common, _ := parseCommonFlags(args)
	if err := applyLogFlags(common, errOut); err != nil {
		return err
	}

	cfg, err := loadConfig(common.configPath)
	if err != nil {
		return err
	}
	client, err := newGHClient(cfg)
	if err != nil {
		return err
	}

	centralOwner, centralRepo, err := detectCentralRepo(cfg, errOut)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), common.timeout)
	defer cancel()

	mon := monitor.New(cfg, client, centralOwner, centralRepo)

	deployState, err := loadDeploymentState(client, centralOwner, centralRepo)
	if err != nil {
		return err
	}

	result, err := mon.Run(ctx, deployState, monitor.Options{
		ConfigPath:   common.configPath,
		TargetFilter: common.targetFilter,
	})
	if err != nil {
		return fmt.Errorf("monitor error: %w", err)
	}

	printJSON(result, out)

	setOutput("repos-in-sync", strconv.Itoa(result.ReposInSync))
	setOutput("repos-drifted", strconv.Itoa(result.ReposDrifted))
	setOutput("issues-created", strconv.Itoa(result.IssuesCreated))
	setOutput("issues-closed", strconv.Itoa(result.IssuesClosed))
	summaryJSON, _ := json.Marshal(result)
	setOutput("summary", string(summaryJSON))
	return nil
}

func runConflict(args []string, out, errOut io.Writer) error {
	common, remaining := parseCommonFlags(args)
	if err := applyLogFlags(common, errOut); err != nil {
		return err
	}
	var tiers []int

	for i := 0; i < len(remaining); i++ {
		if remaining[i] == "--tiers" && i+1 < len(remaining) {
			for _, t := range strings.Split(remaining[i+1], ",") {
				t = strings.TrimSpace(t)
				n, err := strconv.Atoi(t)
				if err != nil {
					return fmt.Errorf("invalid tier: %s", t)
				}
				tiers = append(tiers, n)
			}
			i++
		}
	}

	cfg, err := loadConfig(common.configPath)
	if err != nil {
		return err
	}
	client, err := newGHClient(cfg)
	if err != nil {
		return err
	}

	centralOwner, centralRepo, err := detectCentralRepo(cfg, errOut)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), common.timeout)
	defer cancel()

	scanner := conflict.New(cfg, client, centralOwner, centralRepo)

	deployState, err := loadDeploymentState(client, centralOwner, centralRepo)
	if err != nil {
		return err
	}

	targets, err := scanner.Targets(ctx, deployState, common.targetFilter)
	if err != nil {
		return fmt.Errorf("conflict detection error: %w", err)
	}

	report, err := scanner.Run(ctx, deployState, targets, conflict.Options{
		ConfigPath:   common.configPath,
		TargetFilter: common.targetFilter,
		Tiers:        tiers,
	})
	if err != nil {
		return fmt.Errorf("conflict detection error: %w", err)
	}

	printJSON(report, out)

	setOutput("conflicts-found", strconv.Itoa(len(report.Entries)))
	setOutput("critical-count", strconv.Itoa(report.SeveritySummary.Critical))
	setOutput("warning-count", strconv.Itoa(report.SeveritySummary.Warning))
	setOutput("info-count", strconv.Itoa(report.SeveritySummary.Info))
	reportJSON, _ := json.Marshal(report)
	setOutput("report", string(reportJSON))
	// The actions expose summary, like sync and monitor; report stays for direct callers of the binary (GH #172).
	setOutput("summary", string(reportJSON))

	if report.SeveritySummary.Critical > 0 {
		return fmt.Errorf("conflict detection found %d critical conflict(s)", report.SeveritySummary.Critical)
	}
	return nil
}

func detectTrigger() string {
	if os.Getenv("GITHUB_EVENT_NAME") == "push" {
		return "push"
	}
	if os.Getenv("GITHUB_EVENT_NAME") == "workflow_dispatch" {
		return "manual"
	}
	if os.Getenv("GITHUB_EVENT_NAME") == "schedule" {
		return "schedule"
	}
	return "manual"
}

func detectCentralRepo(cfg *config.SyncConfig, errOut io.Writer) (string, string, error) {
	// 1. Explicit config field takes priority
	if cfg.CentralRepo != "" {
		parts := strings.SplitN(cfg.CentralRepo, "/", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], nil
		}
		return "", "", fmt.Errorf("invalid central-repo value %q: expected owner/repo format", cfg.CentralRepo)
	}

	// 2. Fall back to GITHUB_REPOSITORY env var
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo != "" {
		parts := strings.SplitN(repo, "/", 2)
		if len(parts) == 2 {
			return parts[0], parts[1], nil
		}
	}
	return "", "", fmt.Errorf("cannot determine central repository; set central-repo in config or GITHUB_REPOSITORY env var")
}

// loadDeploymentState reads the deployment state from the central repo. A missing file (404) is a first run and
// yields an empty state; any other read or decode failure is returned, so monitor and conflict do not mistake an
// outage for "nothing is managed": conflict would then report every configured target's files as unmanaged and
// close its open issues on incomplete evidence.
func loadDeploymentState(client *gh.Client, owner, repo string) (*state.DeploymentState, error) {
	ctx := context.Background()
	data, _, err := client.Repos.GetFileContent(ctx, owner, repo, deploymentStatePath, "")
	if err != nil {
		var apiErr *gh.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return state.NewDeploymentState(), nil
		}
		return nil, fmt.Errorf("reading deployment state %s/%s/%s: %w", owner, repo, deploymentStatePath, err)
	}
	ds, err := state.Load(data)
	if err != nil {
		return nil, fmt.Errorf("decoding deployment state %s/%s/%s: %w", owner, repo, deploymentStatePath, err)
	}
	return ds, nil
}

const deploymentStatePath = ".steerspec/deployment-state.json"

func printJSON(v any, w io.Writer) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v) //nolint:errcheck
}
