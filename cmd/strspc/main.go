package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/SteerSpec/strspc-sync/internal/config"
	"github.com/SteerSpec/strspc-sync/internal/conflict"
	gh "github.com/SteerSpec/strspc-sync/internal/github"
	"github.com/SteerSpec/strspc-sync/internal/monitor"
	"github.com/SteerSpec/strspc-sync/internal/state"
	"github.com/SteerSpec/strspc-sync/internal/sync"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "sync":
		runSync(args)
	case "monitor":
		runMonitor(args)
	case "conflict":
		runConflict(args)
	case "version":
		fmt.Printf("strspc %s (commit: %s)\n", version, commit)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: strspc <command> [flags]

Commands:
  sync       Synchronize templates to target repositories
  monitor    Check target repositories for drift
  conflict   Detect conflicts across target repositories
  version    Print version information
  help       Show this help

Sync Flags:
  --config <path>           Config file path (default: steerspec-sync.yml)
  --dry-run                 Preview changes without creating PRs
  --target-filter <glob>    Filter target repositories
  --template-filter <glob>  Filter templates
  --force                   Force sync even if up to date

Monitor Flags:
  --config <path>           Config file path (default: steerspec-sync.yml)
  --target-filter <glob>    Filter target repositories

Conflict Flags:
  --config <path>           Config file path (default: steerspec-sync.yml)
  --target-filter <glob>    Filter target repositories
  --tiers <list>            Detection tiers to run (e.g. "1,2,3")
`)
}

type commonFlags struct {
	configPath   string
	targetFilter string
}

func parseCommonFlags(args []string) (commonFlags, []string) {
	f := commonFlags{configPath: "steerspec-sync.yml"}
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
		default:
			remaining = append(remaining, args[i])
		}
	}
	return f, remaining
}

func loadConfig(path string) *config.SyncConfig {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}
	return cfg
}

func newGHClient(cfg *config.SyncConfig) *gh.Client {
	// For github-token method, resolve token from GITHUB_TOKEN env var if not set in config
	if cfg.Auth.Method == "github-token" && cfg.Auth.Token == "" {
		cfg.Auth.Token = os.Getenv("GITHUB_TOKEN")
	}

	client, err := gh.NewClient(gh.AuthConfig{
		Method:     cfg.Auth.Method,
		AppID:      cfg.Auth.AppID,
		PrivateKey: cfg.Auth.PrivateKey,
		Token:      cfg.Auth.Token,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating GitHub client: %v\n", err)
		os.Exit(1)
	}
	return client
}

func setOutput(key, value string) {
	outputFile := os.Getenv("GITHUB_OUTPUT")
	if outputFile == "" {
		return
	}
	f, err := os.OpenFile(outputFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s=%s\n", key, value)
}

func runSync(args []string) {
	common, remaining := parseCommonFlags(args)
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

	cfg := loadConfig(common.configPath)
	client := newGHClient(cfg)

	syncer := sync.New(cfg, client)
	result, err := syncer.Run(context.Background(), sync.Options{
		ConfigPath:     common.configPath,
		DryRun:         dryRun,
		TargetFilter:   common.targetFilter,
		TemplateFilter: templateFilter,
		Force:          force,
		Trigger:        detectTrigger(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync error: %v\n", err)
		os.Exit(1)
	}

	printJSON(result)

	setOutput("prs-created", strconv.Itoa(result.PRsCreated))
	setOutput("prs-updated", strconv.Itoa(result.PRsUpdated))
	setOutput("repos-skipped", strconv.Itoa(result.ReposSkipped))
	setOutput("errors", strconv.Itoa(result.Errors))
	summaryJSON, _ := json.Marshal(result)
	setOutput("summary", string(summaryJSON))

	if result.Errors > 0 {
		os.Exit(1)
	}
}

func runMonitor(args []string) {
	common, _ := parseCommonFlags(args)

	cfg := loadConfig(common.configPath)
	client := newGHClient(cfg)

	centralOwner, centralRepo := detectCentralRepo()
	mon := monitor.New(cfg, client, centralOwner, centralRepo)

	deployState := loadDeploymentState(client, centralOwner, centralRepo)

	result, err := mon.Run(context.Background(), deployState, monitor.Options{
		ConfigPath:   common.configPath,
		TargetFilter: common.targetFilter,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "monitor error: %v\n", err)
		os.Exit(1)
	}

	printJSON(result)

	setOutput("repos-in-sync", strconv.Itoa(result.ReposInSync))
	setOutput("repos-drifted", strconv.Itoa(result.ReposDrifted))
	setOutput("issues-created", strconv.Itoa(result.IssuesCreated))
	setOutput("issues-closed", strconv.Itoa(result.IssuesClosed))
	summaryJSON, _ := json.Marshal(result)
	setOutput("summary", string(summaryJSON))
}

func runConflict(args []string) {
	common, remaining := parseCommonFlags(args)
	var tiers []int

	for i := 0; i < len(remaining); i++ {
		if remaining[i] == "--tiers" && i+1 < len(remaining) {
			for _, t := range strings.Split(remaining[i+1], ",") {
				t = strings.TrimSpace(t)
				n, err := strconv.Atoi(t)
				if err != nil {
					fmt.Fprintf(os.Stderr, "invalid tier: %s\n", t)
					os.Exit(1)
				}
				tiers = append(tiers, n)
			}
			i++
		}
	}

	cfg := loadConfig(common.configPath)
	client := newGHClient(cfg)

	centralOwner, centralRepo := detectCentralRepo()
	scanner := conflict.New(cfg, client, centralOwner, centralRepo)

	deployState := loadDeploymentState(client, centralOwner, centralRepo)

	// Collect target repo names from deployment state
	var targets []string
	for repo := range deployState.Repositories {
		targets = append(targets, repo)
	}

	report, err := scanner.Run(context.Background(), deployState, targets, conflict.Options{
		ConfigPath:   common.configPath,
		TargetFilter: common.targetFilter,
		Tiers:        tiers,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "conflict detection error: %v\n", err)
		os.Exit(1)
	}

	printJSON(report)

	setOutput("conflicts-found", strconv.Itoa(len(report.Entries)))
	setOutput("critical-count", strconv.Itoa(report.SeveritySummary.Critical))
	setOutput("warning-count", strconv.Itoa(report.SeveritySummary.Warning))
	setOutput("info-count", strconv.Itoa(report.SeveritySummary.Info))
	reportJSON, _ := json.Marshal(report)
	setOutput("report", string(reportJSON))

	if report.SeveritySummary.Critical > 0 {
		os.Exit(1)
	}
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

func detectCentralRepo() (string, string) {
	repo := os.Getenv("GITHUB_REPOSITORY")
	if repo != "" {
		parts := strings.SplitN(repo, "/", 2)
		if len(parts) == 2 {
			return parts[0], parts[1]
		}
	}
	fmt.Fprintf(os.Stderr, "error: cannot determine central repository; set GITHUB_REPOSITORY or run in a GitHub Actions environment\n")
	os.Exit(1)
	return "", ""
}

func loadDeploymentState(client *gh.Client, owner, repo string) *state.DeploymentState {
	ctx := context.Background()
	data, _, err := client.Repos.GetFileContent(ctx, owner, repo, ".steerspec/deployment-state.json", "")
	if err != nil {
		// State file may not exist yet; return empty state
		return state.NewDeploymentState()
	}
	ds, err := state.Load(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not parse deployment state, starting fresh: %v\n", err)
		return state.NewDeploymentState()
	}
	return ds
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
