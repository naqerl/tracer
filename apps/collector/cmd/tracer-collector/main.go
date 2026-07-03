package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"tracer/apps/collector/internal/app"
	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/formatguesser"
	"tracer/apps/collector/internal/health"
	"tracer/apps/collector/internal/install"
	"tracer/apps/collector/internal/lifecycle"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
	"tracer/apps/collector/internal/stats"
	"tracer/apps/collector/internal/transport"
	"tracer/build"
	"tracer/libs/logx"
	sysinstall "tracer/libs/shared/install"
	"tracer/libs/shared/runtime"
	"tracer/libs/shared/update"
	"tracer/libs/types"
)

func main() {
	// Define flags
	var (
		addCmd = flag.NewFlagSet("add", flag.ExitOnError)
		setCmd = flag.NewFlagSet("set", flag.ExitOnError)

		configPath    = flag.String("config", "", "path to config file")
		printDefaults = flag.Bool("print-defaults", false, "print default configuration and exit")
	)
	flag.Parse()

	logx.SetDefault()

	// Resolve config path: flag -> env -> default
	cfgPath := resolveConfigPath(*configPath)

	if *printDefaults {
		if err := printDefaultsCmd(); err != nil {
			log.Fatalf("failed to print defaults: %s", err)
		}
		return
	}

	// Setup context with cancellation on shutdown signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if flag.NArg() > 0 {
		switch flag.Arg(0) {
		case "add":
			cfg, err := config.Load(cfgPath)
			if err != nil {
				log.Fatalf("failed to load config: %s", err)
			}
			handleAdd(ctx, cfgPath, cfg, addCmd)
			return
		case "set":
			cfg, err := config.Load(cfgPath)
			if err != nil {
				log.Fatalf("failed to load config: %s", err)
			}
			handleSet(cfgPath, cfg, setCmd)
			return
		case "update":
			handleUpdate()
			return
		case "install":
			handleInstall()
			return
		default:
			log.Fatalf("Unknown command: %s\n", flag.Arg(0))
		}
	}

	// Default behavior: run the collector
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %s", err)
	}

	sysinstall.RequireServiceUser(build.CollectorUser, build.TracerGroup)

	slog.Info("Starting collector",
		"name", cfg.Transport.Name,
		"panelHost", cfg.Transport.PanelHost,
		"apps", len(cfg.Apps),
	)

	if err := serve(ctx, cfg); err != nil {
		log.Fatalf("failed to serve: %s", err)
	}
}

func printDefaultsCmd() error {
	cfg := config.Default()
	yaml, err := cfg.String()
	if err != nil {
		return err
	}
	fmt.Print(yaml)
	return nil
}

func serve(ctx context.Context, cfg config.Config) error {
	var wg sync.WaitGroup
	mpsc := make(chan types.Event, 1000)

	// Parsers
	parsers := make(map[string]parser.Parser, len(cfg.Parsers))
	for id, spec := range cfg.Parsers {
		if _, exists := parsers[id]; exists {
			return fmt.Errorf("duplicate parser id: %s", id)
		}
		spec.ID = id
		parsers[id] = parser.New(spec)
	}

	// Create apps
	apps := make(map[string]app.App, len(cfg.Apps))
	healthPollers := make(map[string]health.Poller)
	appNames := make([]string, 0, len(cfg.Apps))
	for _, spec := range cfg.Apps {
		if _, exists := apps[spec.Name]; exists {
			return fmt.Errorf("duplicate app name: %s", spec.Name)
		}

		p, ok := parsers[spec.ParserID]
		if !ok {
			return fmt.Errorf("parser with id=%s not found", spec.ParserID)
		}
		s, err := source.New(spec.Source)
		if err != nil {
			return fmt.Errorf("invalid source config for app %s: %w", spec.Name, err)
		}

		lfc, err := lifecycle.New(spec.Lifecycle)
		if err != nil {
			return fmt.Errorf("invalid lifecycle config for app %s: %w", spec.Name, err)
		}

		apps[spec.Name] = app.New(s, p, lfc, spec.Name)
		if spec.Health != nil {
			poller, err := health.New(*spec.Health)
			if err != nil {
				return fmt.Errorf("invalid health config for app %s: %w", spec.Name, err)
			}
			healthPollers[spec.Name] = poller
		}
		appNames = append(appNames, spec.Name)
	}

	// Resource monitor
	statsMonitor := stats.New(cfg.Stats)

	// Transport
	svc := transport.New(cfg.Transport, appNames, mpsc)
	svc.OnUpdate = func() {
		if err := doCollectorUpdate(); err != nil {
			slog.Error("Collector update from panel failed", "error", err)
		}
	}

	// Run
	wg.Go(func() {
		slog.Info("starting transport")
		defer slog.Info("transport stopped")

		svc.Start(ctx)
	})
	wg.Go(func() {
		slog.Info("starting stats monitor")
		defer slog.Info("stats monitor stopped")

		for evt := range statsMonitor.Produce(ctx) {
			select {
			case mpsc <- evt:
			case <-ctx.Done():
				return
			}
		}
	})
	for _, app := range apps {
		app := app // capture
		wg.Go(func() {
			slog.Info("starting app producer", "app", app.Name)
			defer slog.Info("app producer stopped", "app", app.Name)

			for evt := range app.Produce(ctx) {
				select {
				case mpsc <- evt:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	for appName, poller := range healthPollers {
		poller := poller // capture
		wg.Go(func() {
			slog.Info("starting health poller", "app", appName)
			defer slog.Info("health poller stopped", "app", appName)

			for e := range poller.Produce(ctx) {
				select {
				case mpsc <- types.HealthEvent{
					App:    appName,
					Time:   e.Time,
					Status: e.Status,
				}:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	wg.Wait()

	return nil
}

func handleAdd(ctx context.Context, path string, cfg config.Config, addCmd *flag.FlagSet) {
	override := addCmd.Bool("override", false, "override the app unit in the config")
	healthURL := addCmd.String("health", "", "health check URL to attach as an HTTP health probe")
	favourRaw := addCmd.String("favour", "", `favour specific parser settings when conflicts occur (e.g., 'type:"json",level_field:"status"')`)
	noConfirm := addCmd.Bool("no-confirm", false, "skip confirmation prompt and save immediately")
	namespace := addCmd.String("n", "default", "Kubernetes namespace (for kubectl sources)")
	addCmd.Parse(flag.Args()[1:])
	remaining := addCmd.Args()

	if len(remaining) == 0 {
		log.Fatal("Missing source: must be a file path, kubectl resource, or systemd unit name")
	}
	name, spec := inferSourceSpec(remaining[0], *namespace)

	fmt.Printf("Adding log source: %s\n", remaining[0])
	switch spec.Type {
	case "file":
		fmt.Printf("  Type: file\n")
		fmt.Printf("  Path: %s\n", spec.Params["path"])
	case "kubectl":
		fmt.Printf("  Type: Kubernetes resource\n")
		fmt.Printf("  Resource: %s\n", spec.Params["resource"])
		fmt.Printf("  Namespace: %s\n", spec.Params["namespace"])
	default:
		fmt.Printf("  Type: systemd unit\n")
		fmt.Printf("  Unit: %s\n", spec.Params["unit"])
	}

	// Parse the favour spec
	favour, err := parseFavourSpec(*favourRaw)
	if err != nil {
		log.Fatalf("Invalid --favour value: %v", err)
	}

	result, err := formatguesser.Add(ctx, &cfg, name, spec, formatguesser.AddRequest{
		Override: *override,
		Favour:   favour,
	})
	if err != nil {
		fmt.Printf("\nError: Could not analyze log format\n\n")

		// Show specific error details with the problematic line
		var guessErr formatguesser.GuessError
		var consistencyErr formatguesser.ConsistencyError
		if errors.As(err, &guessErr) {
			fmt.Printf("Could not parse this log line:\n")
			fmt.Printf("  %s\n\n", truncateLine(guessErr.Line, 200))
			fmt.Printf("Make sure the log is in JSON or slog text format.\n")
			fmt.Printf("For JSON: fields like level, msg, time, trace_id are expected\n")
			fmt.Printf("For slog text: format like 'time=... level=... msg=...'\n")
		} else if errors.As(err, &consistencyErr) {
			fmt.Printf("Found inconsistent log format\n\n")
			fmt.Printf("First line (established format):\n")
			fmt.Printf("  %s\n\n", truncateLine(consistencyErr.FirstLine, 200))
			fmt.Printf("Conflicting line:\n")
			fmt.Printf("  %s\n\n", truncateLine(consistencyErr.Line, 200))
			fmt.Printf("Issue: %v\n\n", consistencyErr.Unwrap())
			fmt.Printf("To resolve, run again with:\n")
			fmt.Printf("  %s\n\n", consistencyErr.SuggestFavour())
		} else {
			fmt.Printf("%v\n\n", err)
		}
		os.Exit(1)
	}

	parserSpec := result.ParserSpec
	addedApp := result.App

	fmt.Printf("\nDetected log format:\n")
	fmt.Printf("  Parser: %s\n", parserSpec.Type)
	fmt.Printf("  Timestamp field: %s\n", parserSpec.Params["timestamp_field"])
	fmt.Printf("  Timestamp format: %s\n", parserSpec.Params["timestamp_format"])
	fmt.Printf("  Level field: %s\n", parserSpec.Params["level_field"])
	fmt.Printf("  Message field: %s\n", parserSpec.Params["message_field"])
	if traceField := parserSpec.Params["trace_id_field"]; traceField != "" {
		fmt.Printf("  Trace ID field: %s\n", traceField)
	}

	// Show sample line preview
	if len(result.SampleLines) > 0 {
		fmt.Printf("\nSample log line:\n")
		fmt.Printf("  %s\n", truncateLine(result.SampleLines[0], 150))
		if len(result.SampleData) > 0 {
			fmt.Printf("\nParsed fields from sample:\n")
			keys := make([]string, 0, len(result.SampleData))
			for k := range result.SampleData {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("  %s: %s\n", k, truncateLine(result.SampleData[k], 80))
			}
		}
	}

	if strings.TrimSpace(*healthURL) != "" {
		fmt.Printf("\nAttaching health check: %s\n", strings.TrimSpace(*healthURL))
		attachHTTPHealth(&cfg, name, spec, strings.TrimSpace(*healthURL))
	}

	// Confirmation prompt
	if !*noConfirm {
		fmt.Printf("\nSave this configuration to %s? [Y/n]: ", path)
		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "" && response != "y" && response != "yes" {
			fmt.Println("Cancelled.")
			os.Exit(0)
		}
	}

	if err := cfg.Persist(path); err != nil {
		log.Fatalf("Failed to write the file config, error: %v", err)
	}

	fmt.Printf("\nConfiguration saved to: %s\n", path)
	fmt.Printf("\nAdded app configuration:\n")
	fmt.Printf("  Name: %s\n", addedApp.Name)
	fmt.Printf("  Parser ID: %s\n", addedApp.ParserID)
	fmt.Printf("  Source Type: %s\n", addedApp.Source.Type)
	fmt.Printf("\nTo apply the new configuration, restart the collector:\n")
	fmt.Printf("  sudo systemctl restart tracer-collector\n")
}

func truncateLine(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// parseFavourSpec parses a favour spec string like: type:"json",level_field:"status"
// Returns an empty spec if input is empty
func parseFavourSpec(input string) (parser.Spec, error) {
	var spec parser.Spec
	if strings.TrimSpace(input) == "" {
		return spec, nil
	}

	spec.Params = make(map[string]string)

	// Parse key:"value" pairs separated by commas
	// Support both key:"value" and key:value formats
	remaining := input
	for remaining != "" {
		// Find the key
		colonIdx := strings.Index(remaining, ":")
		if colonIdx == -1 {
			break
		}
		key := strings.TrimSpace(remaining[:colonIdx])
		remaining = remaining[colonIdx+1:]

		// Skip whitespace
		remaining = strings.TrimLeft(remaining, " ")

		// Parse the value
		var value string
		if strings.HasPrefix(remaining, `"`) {
			// Quoted value
			endQuote := strings.Index(remaining[1:], `"`)
			if endQuote == -1 {
				return spec, fmt.Errorf("unclosed quote in value for key %q", key)
			}
			value = remaining[1 : endQuote+1]
			remaining = remaining[endQuote+2:]
		} else {
			// Unquoted value - read until comma or end
			commaIdx := strings.Index(remaining, ",")
			if commaIdx == -1 {
				value = strings.TrimSpace(remaining)
				remaining = ""
			} else {
				value = strings.TrimSpace(remaining[:commaIdx])
				remaining = remaining[commaIdx+1:]
			}
		}

		// Store the value
		if key == "type" {
			spec.Type = value
		} else {
			spec.Params[key] = value
		}

		// Skip comma and whitespace
		remaining = strings.TrimLeft(remaining, " ,")
	}

	return spec, nil
}

func handleSet(path string, cfg config.Config, setCmd *flag.FlagSet) {
	setCmd.Parse(flag.Args()[1:])
	remaining := setCmd.Args()

	if len(remaining) < 2 {
		log.Fatal("Usage: tracer-collector set <key> <value>")
	}

	key := remaining[0]
	value := remaining[1]

	switch key {
	case "api-key":
		cfg.Transport.APIKey = value
	case "panel-host":
		cfg.Transport.PanelHost = value
	case "name":
		cfg.Transport.Name = value
	default:
		log.Fatalf("Unknown key: %s. Supported keys: api-key, panel-host, name", key)
	}

	if err := cfg.Persist(path); err != nil {
		log.Fatalf("Failed to write the file config, error: %v", err)
	}

	fmt.Printf("Set %s successfully\n", key)
}

func handleUpdate() {
	if err := doCollectorUpdate(); err != nil {
		log.Fatalf("Update failed: %v", err)
	}
}

// doCollectorUpdate performs the collector self-update: build, verify, install,
// and restart. It is used both for on-demand CLI updates and panel-triggered updates.
func doCollectorUpdate() error {
	collectorMgr := runtime.NewManager(runtime.SlugCollector)
	return update.Update(update.Options{
		BinaryName: update.BinaryCollector,
		VerifyFn:   verifyBinaryUpdate,
		PostUpdate: func() error {
			slog.Info("Collector binary updated, restarting...")
			return collectorMgr.Restart()
		},
	})
}

// verifyBinaryUpdate checks the binary is functional before replacing.
func verifyBinaryUpdate(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--version")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("binary verification failed (exit: %w, output: %s)", err, string(output))
	}
	if len(output) == 0 {
		return fmt.Errorf("binary produced no output for --version")
	}
	fmt.Printf("Verified binary: %s\n", strings.TrimSpace(string(output)))
	return nil
}

func handleInstall() {
	if err := install.Run(); err != nil {
		log.Fatalf("Install failed: %v", err)
	}
}

func inferSourceSpec(raw string, namespace string) (string, source.Spec) {
	// 1. If kubectl is available, check if this resource exists in Kubernetes
	if kubectlAvailable() && kubectlResolves(raw, namespace) {
		return extractResourceName(raw), source.Spec{
			Type: "kubectl",
			Params: map[string]string{
				"resource":  raw,
				"namespace": namespace,
			},
		}
	}

	// 2. Check if the path exists as a file on disk
	if fileExists(raw) {
		return filepath.Base(raw), source.Spec{
			Type: "file",
			Params: map[string]string{
				"path":      raw,
				"read_from": "end",
			},
		}
	}

	// 3. Fallback: treat as a systemd unit
	return trimSystemdUnitSuffix(raw), source.Spec{
		Type: "journalctl",
		Params: map[string]string{
			"unit": raw,
		},
	}
}

func trimSystemdUnitSuffix(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasSuffix(trimmed, ".service") {
		return strings.TrimSuffix(trimmed, ".service")
	}
	return trimmed
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func kubectlAvailable() bool {
	_, err := exec.LookPath("kubectl")
	return err == nil
}

func kubectlResolves(resource, namespace string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", "get", resource, "-n", namespace, "--no-headers", "-o", "name")
	out, err := cmd.Output()
	return err == nil && len(out) > 0
}

// extractResourceName returns the part after the last "/" in a resource reference,
// or the full string if there is no "/".
func extractResourceName(raw string) string {
	if idx := strings.LastIndex(raw, "/"); idx >= 0 {
		return raw[idx+1:]
	}
	return raw
}

func attachHTTPHealth(cfg *config.Config, name string, spec source.Spec, url string) {
	defaults := health.HTTPConfig{}.Default()
	defaults.URL = url
	healthSpec := defaults.Spec()

	for i := range cfg.Apps {
		app := &cfg.Apps[i]
		if app.Name == name {
			app.Health = &healthSpec
			return
		}
		// Also match by source spec in case the app name was generated
		if app.Source.Type == spec.Type {
			switch spec.Type {
			case "kubectl":
				if app.Source.Params["resource"] == spec.Params["resource"] &&
					app.Source.Params["namespace"] == spec.Params["namespace"] {
					app.Health = &healthSpec
					return
				}
			case "file":
				if app.Source.Params["path"] == spec.Params["path"] {
					app.Health = &healthSpec
					return
				}
			default:
				if app.Source.Params["unit"] == spec.Params["unit"] {
					app.Health = &healthSpec
					return
				}
			}
		}
	}

	log.Fatalf("Failed to attach health config: app %q was not found after add", name)
}

// resolveConfigPath resolves the config path from flag, env var, or default.
// Priority: flag > env var > default from install package.
func resolveConfigPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if envPath := os.Getenv(install.ConfigEnvVar); envPath != "" {
		return envPath
	}
	return install.ConfigFilePath
}
