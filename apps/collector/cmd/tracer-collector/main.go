package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tracer/apps/collector"
	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/formatguesser"
	"tracer/apps/collector/internal/install"
	"tracer/apps/collector/internal/kubeconfig"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/reconcile"
	"tracer/apps/collector/internal/sender"
	"tracer/apps/collector/internal/source"
	"tracer/apps/collector/internal/stats"
	"tracer/apps/collector/internal/supervisor"
	"tracer/build"
	"tracer/libs/installx"
	"tracer/libs/logx"
	"tracer/libs/types"
	"tracer/libs/updatex"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	printVersion := flag.Bool("version", false, "print version and exit")

	setCmd := flag.NewFlagSet("set", flag.ExitOnError)
	verifyCmd := flag.NewFlagSet("verify", flag.ExitOnError)
	updateCmd := flag.NewFlagSet("update", flag.ExitOnError)
	installCmd := flag.NewFlagSet("install", flag.ExitOnError)
	setupKubectlCmd := flag.NewFlagSet("kubectl-setup", flag.ExitOnError)
	setupKubectlKubeconfig := setupKubectlCmd.String("kubeconfig", "", "kubeconfig to install (default: auto-detect from $KUBECONFIG, ~/.kube/config, or system locations)")

	commands := []commandHelp{
		{name: "set", args: "<key> <value>", summary: "Set a config value. Supported keys: api-key, panel-host, name"},
		{name: "verify", summary: "Check that the collector can reach the panel and authenticate"},
		{name: "update", summary: "Update the collector binary"},
		{name: "install", summary: "Install the collector service, users, and default config"},
		{name: "kubectl-setup", summary: "Install a kubeconfig the collector can use for kubectl sources", flags: setupKubectlCmd},
	}

	flag.Usage = func() { printUsage(os.Stderr, flag.CommandLine, commands) }
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

	cfgPath := resolveConfigPath(*configPath)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if flag.NArg() > 0 {
		switch flag.Arg(0) {
		case "help":
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
		case "set":
			if wantsHelp(flag.Args()[1:], setCmd, false) {
				setCmd.Usage()
				return
			}
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

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %s", err)
	}

	installx.RequireServiceUser(build.CollectorUser, build.TracerGroup)

	slog.Info("Starting collector",
		"name", cfg.Sender.Name,
		"panelHost", cfg.Sender.PanelHost,
	)

	if err := serve(ctx, cfg); err != nil {
		log.Fatalf("failed to serve: %s", err)
	}
}

func serve(ctx context.Context, cfg config.Config) error {
	mpsc := make(chan types.Event, 1000)

	senderSvc := sender.New(cfg.Sender, mpsc)
	sup := supervisor.New()

	statsCtx, statsCancel := context.WithCancel(ctx)
	applyStats := func(sc types.StatsConfig) {
		statsCancel()
		statsCtx, statsCancel = context.WithCancel(ctx)
		interval, err := time.ParseDuration(sc.Interval)
		if err != nil || interval <= 0 {
			interval = 30 * time.Second
		}
		monitor := stats.New(stats.Spec{
			StatsInterval: interval,
			CPU:           sc.CPU,
			Mem:           sc.Mem,
			Disk:          sc.Disk,
		})
		go func() {
			for evt := range monitor.Produce(statsCtx) {
				select {
				case mpsc <- evt:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	applyStats(types.DefaultStatsConfig())

	go func() {
		slog.Info("starting sender")
		defer slog.Info("sender stopped")
		senderSvc.Start(ctx)
	}()

	var current reconcile.Config
	lastAppSet := ""
	for {
		select {
		case <-ctx.Done():
			sup.Stop()
			if ctx.Err() == context.Canceled {
				return nil
			}
			return ctx.Err()
		case evt, ok := <-senderSvc.Recv():
			if !ok {
				return nil
			}
			switch e := evt.(type) {
			case types.ConfigEvent:
				var cc types.CollectorConfig
				if err := json.Unmarshal([]byte(e.Config), &cc); err != nil {
					slog.Error("could not decode pushed config, ignoring", "error", err)
					continue
				}
				if err := cc.Validate(); err != nil {
					slog.Error("pushed config is invalid, ignoring", "error", err)
					continue
				}
				applyStats(cc.GetStats())
				current = reconcile.Config{
					Apps:    cc.Apps,
					Parsers: adapterParsers(cc.Parsers),
				}
				if err := reconcile.Apply(ctx, sup, mpsc, current); err != nil {
					slog.Error("could not apply pushed config, keeping current", "error", err)
					continue
				}
				slog.Info("applied panel-pushed config", "apps", len(current.Apps))

				names := reconcile.Names(current)
				set := strings.Join(names, "\x00")
				if set != lastAppSet {
					lastAppSet = set
					if err := senderSvc.Register(names); err != nil {
						slog.Warn("could not re-register updated app set", "error", err)
					}
				}
			case types.FormatRequestEvent:
				handleFormatRequest(ctx, senderSvc, e)
			}
		}
	}
}

// handleFormatRequest runs the format guesser for a named service and sends
// the result back to the panel over the same WebSocket connection.
func handleFormatRequest(ctx context.Context, senderSvc *sender.Service, req types.FormatRequestEvent) {
	result := types.FormatResultEvent{
		Type:      types.EventTypeFormatResult,
		RequestID: req.RequestID,
	}

	// Prefer journalctl; fall back to kubectl when the service is not a
	// systemd unit. A found-but-failed guess (conflict, missing fields) is
	// reported as-is instead of masking it with the other source type.
	guessed, err := guessService(ctx, req, "journalctl", map[string]string{"unit": req.Service})
	if err != nil {
		if sourceNotFound(err) {
			guessed, err = guessService(ctx, req, "kubectl", map[string]string{"resource": req.Service})
		}
	}
	if err != nil {
		if c := conflictFrom(err); c != nil {
			result.Conflict = c
		} else {
			result.Error = err.Error()
			result.NotFound = sourceNotFound(err)
		}
		if sendErr := senderSvc.Send(result); sendErr != nil {
			slog.Error("could not send format result", "error", sendErr)
		}
		return
	}

	result.App = guessed.App
	result.Parser = types.ParserSpec{
		Type:   guessed.ParserSpec.Type,
		Params: guessed.ParserSpec.Params,
	}
	result.Samples = guessed.SampleLines
	result.Fields = guessed.SampleData
	if sendErr := senderSvc.Send(result); sendErr != nil {
		slog.Error("could not send format result", "error", sendErr)
	}
}

// guessService runs the format guesser against one source type for the named
// service, passing the request's favour so conflicts resolve deterministically.
func guessService(ctx context.Context, req types.FormatRequestEvent, srcType string, params map[string]string) (*formatguesser.AddResult, error) {
	spec := source.Spec{Type: srcType, Params: params}
	favour := formatguesser.AddRequest{Favour: parser.Spec{Type: req.Favour.Type, Params: req.Favour.Params}}
	return formatguesser.Add(ctx, &types.CollectorConfig{}, req.App, spec, favour)
}

// sourceNotFound reports whether the guess failed before reading any log
// line. Typed guess errors (unknown format, missing fields, inconsistency)
// imply the source was found and produced input.
func sourceNotFound(err error) bool {
	var g formatguesser.GuessError
	var c formatguesser.ConsistencyError
	var m *formatguesser.MissingFieldsError
	return !errors.As(err, &g) && !errors.As(err, &c) && !errors.As(err, &m)
}

// conflictFrom extracts a structured format conflict from a guess error, or
// nil when the error does not describe an inconsistency.
func conflictFrom(err error) *types.FormatConflict {
	var c formatguesser.ConsistencyError
	if !errors.As(err, &c) {
		return nil
	}
	options := make([]string, 0, 2)
	for _, v := range []string{c.PrevValue, c.CurrValue} {
		if v != "" && (len(options) == 0 || options[len(options)-1] != v) {
			options = append(options, v)
		}
	}
	return &types.FormatConflict{
		Field:     c.Field,
		PrevValue: c.PrevValue,
		CurrValue: c.CurrValue,
		Options:   options,
		FirstLine: c.FirstLine,
		CurrLine:  c.Line,
	}
}

// adapterParsers converts shared ParserSpecs to collector-internal parser.Specs.
func adapterParsers(shared map[string]types.ParserSpec) map[string]parser.Spec {
	out := make(map[string]parser.Spec, len(shared))
	for id, s := range shared {
		out[id] = parser.Spec{ID: id, Type: s.Type, Params: s.Params}
	}
	return out
}

func wantsHelp(args []string, fs *flag.FlagSet, reorder bool) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
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
		if f := fs.Lookup(name); f != nil {
			if bv, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !bv.IsBoolFlag() {
				if !hasInlineValue {
					i++
				}
			}
		}
		if !reorder && (!strings.HasPrefix(a, "-") || a == "-") {
			return false
		}
	}
	return false
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
	verifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := sender.Verify(verifyCtx, cfg.Sender, nil); err != nil {
		log.Fatalf("Verification failed: %v", err)
	}

	fmt.Printf("Verification successful: connected to panel at %s\n", cfg.Sender.PanelHost)
}

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

	fmt.Printf("Kubectl access configured for the collector: %s\n", kubeconfig.Path)
	fmt.Printf("Restart the collector to apply: sudo systemctl restart tracer-collector\n")
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

func kubectlAvailable() bool {
	_, err := exec.LookPath("kubectl")
	return err == nil
}

func resolveConfigPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if envPath := os.Getenv(install.ConfigEnvVar); envPath != "" {
		return envPath
	}
	return install.ConfigFilePath
}
