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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"tracer/apps/collector"
	"tracer/apps/collector/internal/app"
	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/formatguesser"
	"tracer/apps/collector/internal/health"
	"tracer/apps/collector/internal/install"
	"tracer/apps/collector/internal/kubeconfig"
	"tracer/apps/collector/internal/lifecycle"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/sender"
	"tracer/apps/collector/internal/source"
	"tracer/apps/collector/internal/stats"
	"tracer/build"
	"tracer/libs/installx"
	"tracer/libs/logx"
	"tracer/libs/types"
	"tracer/libs/updatex"
)

func main() {
	// Global flags
	configPath := flag.String("config", "", "path to config file")
	printDefaults := flag.Bool("print-defaults", false, "print default configuration and exit")
	printVersion := flag.Bool("version", false, "print version and exit")

	// Subcommand flag sets. Flags are registered up front (not lazily inside
	// the handlers) so `-help` can enumerate the complete CLI interface.
	addOpts := &addOptions{}
	addCmd := newAddFlagSet(addOpts)
	setCmd := flag.NewFlagSet("set", flag.ExitOnError)
	verifyCmd := flag.NewFlagSet("verify", flag.ExitOnError)
	updateCmd := flag.NewFlagSet("update", flag.ExitOnError)
	installCmd := flag.NewFlagSet("install", flag.ExitOnError)
	setupKubectlCmd := flag.NewFlagSet("kubectl-setup", flag.ExitOnError)
	setupKubectlKubeconfig := setupKubectlCmd.String("kubeconfig", "", "kubeconfig to install (default: auto-detect from $KUBECONFIG, ~/.kube/config, or system locations)")

	commands := []commandHelp{
		{name: "add", args: "<source>", summary: "Add a log source: a systemd unit, a log file path, or a Kubernetes resource (e.g. deployment/myapp)", flags: addCmd},
		{name: "set", args: "<key> <value>", summary: "Set a config value. Supported keys: api-key, panel-host, name"},
		{name: "verify", summary: "Check that the collector can reach the panel, authenticate, and register"},
		{name: "update", summary: "Update the collector binary"},
		{name: "install", summary: "Install the collector service, users, and default config"},
		{name: "kubectl-setup", summary: "Install a kubeconfig the collector can use for kubectl sources", flags: setupKubectlCmd},
	}

	// Complete help: -help/-h show the full CLI. The flag package calls
	// flag.Usage both for an explicit -help and for parse errors, so it
	// writes to stderr (matching Go's flag conventions); the `help` command
	// and early wantsHelp exits write to stdout for piping.
	flag.Usage = func() { printUsage(os.Stderr, flag.CommandLine, commands) }
	setCommandUsage(addCmd, "tracer-collector add [flags] <source>")
	setCommandUsage(setCmd, "tracer-collector set <key> <value>")
	setCommandUsage(verifyCmd, "tracer-collector verify")
	setCommandUsage(updateCmd, "tracer-collector update")
	setCommandUsage(installCmd, "tracer-collector install")
	setCommandUsage(setupKubectlCmd, "tracer-collector kubectl-setup [flags]")

	flag.Parse()

	if *printVersion {
		fmt.Printf("tracer-collector %s\n", collector.Version)
		return
	}

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
		case "help":
			// `help [command]` delegates to a single command's usage when a
			// known command is named, otherwise prints the full interface.
			if len(flag.Args()) > 1 {
				for _, c := range commands {
					if c.name == flag.Arg(1) {
						printCommandUsage(c)
						return
					}
				}
			}
			printUsage(os.Stdout, flag.CommandLine, commands)
			return
		case "add":
			// add reorders flags before parsing, so help may follow the
			// positional source argument.
			if wantsHelp(flag.Args()[1:], addCmd, true) {
				addCmd.Usage()
				return
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				log.Fatalf("failed to load config: %s", err)
			}
			handleAdd(ctx, cfgPath, cfg, addCmd, addOpts)
			return
		case "set":
			// set parses without reordering: flags after the first positional
			// are values, not help requests.
			if wantsHelp(flag.Args()[1:], setCmd, false) {
				setCmd.Usage()
				return
			}
			// set mutates the config, which is owned by the service user.
			// Check privileges before loading so a plain user gets a clear
			// error instead of a raw permission denied.
			installx.RequireRootOrServiceUser(build.CollectorUser, build.TracerGroup)
			cfg, err := config.Load(cfgPath)
			if err != nil {
				log.Fatalf("failed to load config: %s", err)
			}
			handleSet(cfgPath, cfg, setCmd)
			return
		case "verify":
			if wantsHelp(flag.Args()[1:], verifyCmd, false) {
				verifyCmd.Usage()
				return
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				log.Fatalf("failed to load config: %s", err)
			}
			handleVerify(ctx, cfg)
			return
		case "update":
			if wantsHelp(flag.Args()[1:], updateCmd, false) {
				updateCmd.Usage()
				return
			}
			handleUpdate()
			return
		case "install":
			if wantsHelp(flag.Args()[1:], installCmd, false) {
				installCmd.Usage()
				return
			}
			handleInstall()
			return
		case "kubectl-setup":
			if wantsHelp(flag.Args()[1:], setupKubectlCmd, false) {
				setupKubectlCmd.Usage()
				return
			}
			setupKubectlCmd.Parse(flag.Args()[1:])
			handleKubectlSetup(cfgPath, *setupKubectlKubeconfig)
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

	installx.RequireServiceUser(build.CollectorUser, build.TracerGroup)

	slog.Info("Starting collector",
		"name", cfg.Sender.Name,
		"panelHost", cfg.Sender.PanelHost,
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

	// Sender
	senderSvc := sender.New(cfg.Sender, appNames, mpsc)

	// Run
	wg.Go(func() {
		slog.Info("starting sender")
		defer slog.Info("sender stopped")

		senderSvc.Start(ctx)
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

// wantsHelp reports whether args explicitly request help (-h/-help/--help).
// It is checked before a subcommand loads config or enforces privileges, so
// `tracer-collector add -help` works even when the config is missing or the
// caller lacks the service user's permissions. A -h that is the value of a
// value-taking flag (e.g. `add --favour -h`) is not help — mirroring how the
// flag parser treats it.
//
// reorder mirrors whether the command's dispatch calls reorderFlags before
// flag.Parse. Commands that reorder (add) promote flags that follow the
// positional argument, so wantsHelp scans every token; commands that parse
// in place (set, verify, update, install, kubectl-setup) stop at the first
// positional, because Go's flag parser stops there and everything after is a
// value or a positional argument, not a help request.
func wantsHelp(args []string, fs *flag.FlagSet, reorder bool) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		// -h/-help/--help, exact or with =value: the flag package treats
		// -help=... as ErrHelp when no such flag is registered (none of the
		// command flags are named h or help). The =form check only applies
		// to dash-prefixed tokens — a plain positional word "help" is not a
		// help request.
		isFlagToken := strings.HasPrefix(a, "-") && a != "-"
		name := strings.TrimLeft(a, "-")
		hasInlineValue := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
			hasInlineValue = true
		}
		if a == "-h" || a == "-help" || a == "--help" || (isFlagToken && (name == "h" || name == "help")) {
			return true
		}
		// A value-taking flag's value is the next token unless given inline
		// with `--flag=value`; skip that value, it is not a flag.
		if f := fs.Lookup(name); f != nil {
			if bv, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !bv.IsBoolFlag() {
				if !hasInlineValue {
					i++
				}
			}
		}
		// In-place parsers stop at the first positional; anything after it
		// is an argument, not a flag.
		if !reorder && (!strings.HasPrefix(a, "-") || a == "-") {
			return false
		}
	}
	return false
}

// reorderFlags moves all flag tokens (and the value of value-taking flags) to
// the front of args so Go's flag parser sees them before the first positional
// argument. Go's flag package stops at the first non-flag token, which would
// otherwise silently ignore flags placed after the source (e.g. `add
// myapp --no-confirm`).
func reorderFlags(args []string, fs *flag.FlagSet) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			hasInlineValue := false
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
				hasInlineValue = true
			}
			if f := fs.Lookup(name); f != nil {
				if bv, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !bv.IsBoolFlag() {
					// Value-taking flag: the value is the next token unless it
					// was already given inline with `--flag=value`.
					if !hasInlineValue && i+1 < len(args) {
						i++
						flags = append(flags, args[i])
					}
				}
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

// addOptions holds the flags of the `add` command. Flags are registered in
// main (via newAddFlagSet) so -help can enumerate them.
type addOptions struct {
	override   bool
	healthURL  string
	favourRaw  string
	noConfirm  bool
	namespace  string
	kubeconfig string
}

// newAddFlagSet registers the `add` flags on a FlagSet, storing parsed values
// into opts. It must be called exactly once per process.
func newAddFlagSet(opts *addOptions) *flag.FlagSet {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	fs.BoolVar(&opts.override, "override", false, "override the app unit in the config")
	fs.StringVar(&opts.healthURL, "health", "", "health check URL to attach as an HTTP health probe")
	fs.StringVar(&opts.favourRaw, "favour", "", `favour specific parser settings when conflicts occur (e.g., 'type:"json",level_field:"status"')`)
	fs.BoolVar(&opts.noConfirm, "no-confirm", false, "skip confirmation prompt and save immediately")
	fs.StringVar(&opts.namespace, "n", "default", "Kubernetes namespace (for kubectl sources)")
	fs.StringVar(&opts.kubeconfig, "kubeconfig", "", "kubeconfig to install for the collector (default: auto-detect)")
	return fs
}

func handleAdd(ctx context.Context, path string, cfg config.Config, addCmd *flag.FlagSet, opts *addOptions) {
	addCmd.Parse(reorderFlags(flag.Args()[1:], addCmd))
	remaining := addCmd.Args()

	if len(remaining) != 1 {
		log.Fatalf("Unexpected arguments: %v. Usage: tracer-collector add [flags] <source>", remaining)
	}
	name, spec := inferSourceSpec(remaining[0], opts.namespace)

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

	// The daemon runs as the collector service user, which has no home
	// directory or kubeconfig of its own. Install a kubeconfig it can read
	// and point the source at it, so kubectl keeps working after add.
	if spec.Type == "kubectl" {
		installed, err := kubeconfig.Ensure(opts.kubeconfig)
		switch {
		case err == nil:
			spec.Params["kubeconfig"] = installed
			if err := kubeconfig.Resolves(installed, spec.Params["resource"], spec.Params["namespace"]); err != nil {
				log.Fatalf("The kubeconfig installed at %s does not work for this source: %v\nRun `sudo tracer-collector kubectl-setup --kubeconfig <path>` with a kubeconfig that can read %s.",
					installed, err, spec.Params["resource"])
			}
		case errors.Is(err, kubeconfig.ErrRequiresRoot):
			// The current user has a kubeconfig but only root can install it
			// for the daemon. Failing here beats saving a config that cannot
			// work and only breaking at runtime.
			src := opts.kubeconfig
			if src == "" {
				if cur, cerr := kubeconfig.Current(); cerr == nil {
					src = cur
				}
			}
			log.Fatalf("Installing the collector's kubeconfig requires root.\n  Run: sudo tracer-collector kubectl-setup --kubeconfig %s\n  Then re-run: tracer-collector add %s", src, remaining[0])
		default:
			// No kubeconfig found anywhere (e.g. in-cluster or fake kubectl
			// that works without one): warn and continue.
			fmt.Fprintf(os.Stderr, "\nWarning: could not find a kubeconfig for the collector (%v).\n", err)
			fmt.Fprintf(os.Stderr, "Kubectl sources may not work until you run: sudo tracer-collector kubectl-setup\n\n")
		}
	}

	// Parse the favour spec
	favour, err := parseFavourSpec(opts.favourRaw)
	if err != nil {
		log.Fatalf("Invalid --favour value: %v", err)
	}

	result, err := formatguesser.Add(ctx, &cfg, name, spec, formatguesser.AddRequest{
		Override: opts.override,
		Favour:   favour,
	})
	if err != nil {
		fmt.Printf("\nError: Could not analyze log format\n\n")

		// Show specific error details with the problematic line
		var guessErr formatguesser.GuessError
		var consistencyErr formatguesser.ConsistencyError
		switch {
		case errors.As(err, &guessErr):
			fmt.Printf("Could not parse this log line:\n")
			fmt.Printf("  %s\n\n", truncateLine(guessErr.Line, 200))
			fmt.Printf("Reason: %s\n\n", describeGuessFailure(guessErr.Unwrap()))

			var mf *formatguesser.MissingFieldsError
			if errors.As(guessErr.Unwrap(), &mf) {
				requiredNames := make([]string, 0, len(mf.Fields))
				for _, f := range mf.Fields {
					requiredNames = append(requiredNames, fieldDisplayName(f))
				}
				fmt.Printf("Fields with %s are required\n\n", strings.Join(requiredNames, ", "))
				if len(mf.Optional) > 0 {
					names := make([]string, 0, len(mf.Optional))
					for _, f := range mf.Optional {
						names = append(names, fieldDisplayName(f))
					}
					fmt.Printf("Optional field(s) not found: %s (may be ignored)\n\n", strings.Join(names, ", "))
				}
				fmt.Printf("Available keys in the log line:\n")
				fmt.Printf("  %s\n\n", strings.Join(mf.Keys, ", "))

				// Offer an interactive field mapping before giving up, just
				// like the conflict path offers a favour choice. The prompt
				// mapping wins over any explicit --favour the user passed.
				if favourSpec, ok := promptMissingFieldMapping(mf.Fields, mf.Optional, mf.Keys); ok {
					merged := favour
					if merged.Params == nil {
						merged.Params = make(map[string]string)
					}
					for k, v := range favourSpec.Params {
						merged.Params[k] = v
					}
					fmt.Printf("Retrying with %s...\n\n", formatFavourSpec(merged))
					result, err = formatguesser.Add(ctx, &cfg, name, spec, formatguesser.AddRequest{
						Override: opts.override,
						Favour:   merged,
					})
					if err == nil {
						break // retry succeeded; continue to the success output below
					}
					fmt.Printf("Still failed: %v\n\n", err)
				}
			}
		case errors.As(err, &consistencyErr):
			fmt.Printf("Found inconsistent log format\n\n")
			fmt.Printf("First line (established format):\n")
			fmt.Printf("  %s\n\n", truncateLine(consistencyErr.FirstLine, 200))
			fmt.Printf("Conflicting line:\n")
			fmt.Printf("  %s\n\n", truncateLine(consistencyErr.Line, 200))
			fmt.Printf("Issue: %v\n\n", consistencyErr.Unwrap())

			// Offer an interactive selection of the favour before giving up.
			if favourSpec, ok := promptFavour(consistencyErr); ok {
				if favourSpec.Type != "" {
					fmt.Printf("Retrying with format %q...\n\n", favourSpec.Type)
				} else {
					fmt.Printf("Retrying with %s=%q...\n\n", consistencyErr.Field, favourSpec.Params[consistencyErr.Field])
				}
				result, err = formatguesser.Add(ctx, &cfg, name, spec, formatguesser.AddRequest{
					Override: opts.override,
					Favour:   favourSpec,
				})
				if err == nil {
					break // retry succeeded; continue to the success output below
				}
				fmt.Printf("Still failed: %v\n\n", err)
			}

			fmt.Printf("To resolve non-interactively, run again with:\n")
			fmt.Printf("  %s\n\n", consistencyErr.SuggestFavour())
		default:
			fmt.Printf("%v\n\n", err)
		}
		if result == nil {
			os.Exit(1)
		}
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

	if strings.TrimSpace(opts.healthURL) != "" {
		fmt.Printf("\nAttaching health check: %s\n", strings.TrimSpace(opts.healthURL))
		attachHTTPHealth(&cfg, name, spec, strings.TrimSpace(opts.healthURL))
	}

	// Confirmation prompt
	if !opts.noConfirm {
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

// describeGuessFailure turns a guess failure into a precise, user-facing
// statement of which required fields are missing. MissingFieldsError carries
// the field keys as structured data, so no error-message parsing is needed.
// trace_id is optional and never listed; level, message, and timestamp are
// required. Unknown errors pass through unchanged.
func describeGuessFailure(err error) string {
	var mf *formatguesser.MissingFieldsError
	if errors.As(err, &mf) {
		// The guesser reports missing fields in map-iteration order; sort so
		// the message is stable: level, message, timestamp.
		names := make([]string, 0, len(mf.Fields))
		for _, f := range mf.Fields {
			names = append(names, fieldDisplayName(f))
		}
		sort.Strings(names)
		return "missing required field(s): " + strings.Join(names, ", ")
	}
	return err.Error()
}

func fieldDisplayName(field string) string {
	return strings.TrimSuffix(field, "_field")
}

// stdinIsTerminal reports whether stdin is a real interactive terminal. The
// ModeCharDevice check alone is not enough: /dev/null is also a character
// device but reading from it yields immediate EOF, which would print prompts
// into non-interactive output.
func stdinIsTerminal() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return err == nil
}

// promptFavour interactively asks the user which parser setting to prefer when
// the guesser hit an inconsistency (mixed formats or a field that changes
// between lines). It returns the chosen favour spec and true when the user
// picked an option, or false when they skipped the prompt (or stdin is not a
// terminal). The returned spec can be passed straight back to formatguesser.Add.
func promptFavour(ce formatguesser.ConsistencyError) (parser.Spec, bool) {
	if !stdinIsTerminal() {
		return parser.Spec{}, false
	}

	options := []string{ce.PrevValue, ce.CurrValue}
	if ce.Field == "type" {
		fmt.Printf("Which log format should be used?\n")
	} else {
		fmt.Printf("Which field should be used for %s?\n", ce.Field)
	}
	for i, opt := range options {
		fmt.Printf("  %d) %s\n", i+1, opt)
	}
	fmt.Printf("Select a number (or press Enter to skip): ")

	reader := bufio.NewReader(os.Stdin)
	response, _ := reader.ReadString('\n')
	return parseFavourChoice(ce, strings.TrimSpace(response))
}

// parseFavourChoice maps a user's interactive answer to a favour spec.
// An empty response skips (returns false); an invalid number is reported and
// skipped. The two offered options are the previous and the conflicting value.
func parseFavourChoice(ce formatguesser.ConsistencyError, response string) (parser.Spec, bool) {
	if response == "" {
		return parser.Spec{}, false
	}

	options := []string{ce.PrevValue, ce.CurrValue}
	idx, err := strconv.Atoi(response)
	if err != nil || idx < 1 || idx > len(options) {
		fmt.Printf("Invalid selection %q.\n", response)
		return parser.Spec{}, false
	}

	chosen := options[idx-1]
	if ce.Field == "type" {
		return parser.Spec{Type: chosen}, true
	}
	return parser.Spec{Params: map[string]string{ce.Field: chosen}}, true
}

// promptMissingFieldMapping interactively asks the user which key in the log
// line maps to each missing field. Required fields (level, message, timestamp)
// must each be mapped to proceed; optional fields (e.g. trace_id) may be
// ignored by pressing Enter. It returns a favour spec and true when the user
// answered for every required field, or false when they skipped a required
// field (or stdin is not a terminal). The returned spec can be passed straight
// back to formatguesser.Add.
func promptMissingFieldMapping(required, optional, keys []string) (parser.Spec, bool) {
	if !stdinIsTerminal() {
		return parser.Spec{}, false
	}
	if len(keys) == 0 {
		return parser.Spec{}, false
	}

	spec := parser.Spec{Params: make(map[string]string)}
	reader := bufio.NewReader(os.Stdin)

	// Required fields: each one must be mapped or the user gives up.
	for _, field := range required {
		key, ok := askFieldMapping(reader, field, keys, false)
		if !ok {
			return parser.Spec{}, false
		}
		spec.Params[field] = key
	}

	// Optional fields: map them or press Enter to ignore their absence.
	for _, field := range optional {
		if key, ok := askFieldMapping(reader, field, keys, true); ok {
			spec.Params[field] = key
		}
	}

	return spec, true
}

// askFieldMapping prints one field-mapping question and reads the answer. It
// returns the chosen key and true, or ("", false) when the user pressed Enter
// to skip/ignore. An invalid selection re-prompts rather than aborting the
// whole flow.
func askFieldMapping(reader *bufio.Reader, field string, keys []string, ignorable bool) (string, bool) {
	fmt.Printf("Which field should be used for %s?\n", fieldDisplayName(field))
	for i, k := range keys {
		fmt.Printf("  %d) %s\n", i+1, k)
	}
	if ignorable {
		fmt.Printf("Select a number (or press Enter to ignore): ")
	} else {
		fmt.Printf("Select a number (or press Enter to skip): ")
	}

	for {
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(response)
		if response == "" {
			return "", false
		}
		idx, err := strconv.Atoi(response)
		if err != nil || idx < 1 || idx > len(keys) {
			fmt.Printf("Invalid selection %q. ", response)
			if ignorable {
				fmt.Printf("Enter a number or press Enter to ignore: ")
			} else {
				fmt.Printf("Enter a number or press Enter to skip: ")
			}
			continue
		}
		return keys[idx-1], true
	}
}

// formatFavourSpec renders a parser.Spec as a `--favour '...'` value, the
// inverse of parseFavourSpec.
func formatFavourSpec(spec parser.Spec) string {
	var parts []string
	if spec.Type != "" {
		parts = append(parts, fmt.Sprintf("type:%q", spec.Type))
	}
	keys := make([]string, 0, len(spec.Params))
	for k := range spec.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%q", k, spec.Params[k]))
	}
	return "--favour '" + strings.Join(parts, ",") + "'"
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
		cfg.Sender.APIKey = value
	case "panel-host":
		cfg.Sender.PanelHost = value
	case "name":
		cfg.Sender.Name = value
	default:
		log.Fatalf("Unknown key: %s. Supported keys: api-key, panel-host, name", key)
	}

	if err := cfg.Persist(path); err != nil {
		log.Fatalf("Failed to write the file config, error: %v", err)
	}

	fmt.Printf("Set %s successfully\n", key)
}

func handleVerify(ctx context.Context, cfg config.Config) {
	appNames := make([]string, 0, len(cfg.Apps))
	for _, app := range cfg.Apps {
		appNames = append(appNames, app.Name)
	}
	if len(appNames) == 0 {
		fmt.Fprintln(os.Stderr, "Warning: no apps configured; verify checks connectivity only")
	}

	verifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := sender.Verify(verifyCtx, cfg.Sender, appNames); err != nil {
		log.Fatalf("Verification failed: %v", err)
	}

	fmt.Printf("Verification successful: connected to panel at %s, authenticated, and registered\n", cfg.Sender.PanelHost)
}

// handleKubectlSetup installs a kubeconfig the collector service user can
// read, so kubectl sources keep working under the daemon. It verifies the
// source config, copies it to /etc/tracer/kubeconfig, verifies the copy, and
// points every existing kubectl source in the collector config at the
// installed file — so a kubectl source can never silently fall back to an
// unreadable system kubeconfig.
func handleKubectlSetup(cfgPath, kubeconfigPath string) {
	if os.Getuid() != 0 {
		log.Fatalf("kubectl-setup must be run as root: sudo tracer-collector kubectl-setup")
	}
	if !kubectlAvailable() {
		log.Fatalf("kubectl binary not found in PATH; install kubectl or k3s first")
	}

	src := kubeconfigPath
	if src == "" {
		var err error
		src, err = kubeconfig.Current()
		if err != nil {
			log.Fatalf("Could not find a kubeconfig to install: %v\nPass one explicitly: sudo tracer-collector kubectl-setup --kubeconfig <path>", err)
		}
	}

	fmt.Printf("Verifying kubectl access with %s...\n", src)
	if err := kubeconfig.Verify(src); err != nil {
		log.Fatalf("kubectl does not work with %s: %v", src, err)
	}

	if err := kubeconfig.Install(src); err != nil {
		log.Fatalf("Could not install the kubeconfig: %v", err)
	}

	fmt.Printf("Verifying installed kubeconfig %s...\n", kubeconfig.Path)
	if err := kubeconfig.Verify(kubeconfig.Path); err != nil {
		log.Fatalf("The installed kubeconfig does not work: %v", err)
	}

	// Point existing kubectl sources at the installed file so they use it
	// instead of the (unreadable) system default. Config may be absent on a
	// fresh install; the next `add` wires new sources.
	if installx.FileExists(cfgPath) {
		if err := wireKubeconfigIntoConfig(cfgPath); err != nil {
			log.Fatalf("Could not update %s: %v", cfgPath, err)
		}
	}

	fmt.Printf("Kubectl access configured for the collector: %s\n", kubeconfig.Path)
	fmt.Printf("Kubectl sources added via `tracer-collector add` will use it automatically.\n")
	fmt.Printf("Restart the collector to apply: sudo systemctl restart tracer-collector\n")
}

// wireKubeconfigIntoConfig sets params.kubeconfig on every kubectl source in
// the collector config that does not already set it, and persists the result.
func wireKubeconfigIntoConfig(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("could not load config: %w", err)
	}

	if !wireKubeconfig(&cfg) {
		return nil
	}
	if err := cfg.Persist(cfgPath); err != nil {
		return fmt.Errorf("could not persist config: %w", err)
	}
	fmt.Printf("Updated %s: kubectl sources now use %s\n", cfgPath, kubeconfig.Path)
	return nil
}

// wireKubeconfig mutates cfg in place, setting params.kubeconfig on every
// kubectl source that does not already set it. It reports whether anything
// changed.
func wireKubeconfig(cfg *config.Config) bool {
	changed := false
	for i := range cfg.Apps {
		app := &cfg.Apps[i]
		if app.Source.Type != "kubectl" {
			continue
		}
		if app.Source.Params == nil {
			app.Source.Params = make(map[string]string)
		}
		if strings.TrimSpace(app.Source.Params["kubeconfig"]) == "" {
			app.Source.Params["kubeconfig"] = kubeconfig.Path
			changed = true
		}
	}
	return changed
}

func handleUpdate() {
	if err := updatex.Update(updatex.Options{
		BinaryName: updatex.BinaryCollector,
		PostUpdate: func() {
			fmt.Println("Please restart the collector to apply the update:")
			fmt.Println()
			fmt.Println("  sudo systemctl restart tracer-collector")
			fmt.Println()
		},
	}); err != nil {
		log.Fatalf("Update failed: %v", err)
	}
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
