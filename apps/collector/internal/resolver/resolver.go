type ResolvedApp struct {
        Name string
        Parser parser.Parser
        Source source.Source
}

// Resolve instantiates all pipeline components
func (c Config) ResolveApps() ([]ResolvedApp, error) {
	// Build source config lookup map
	sourceConfigByID := make(map[string]sourceConfig)

	for _, src := range c.Sources.File {
		readFrom := src.ReadFrom
		if readFrom == "" {
			readFrom = "end"
		}
		if readFrom != "start" && readFrom != "end" {
			return nil, fmt.Errorf("sources.file[%s].read_from must be 'start' or 'end'", src.ID)
		}
		sourceConfigByID[src.ID] = sourceConfig{
			kind:     "file",
			path:     src.Path,
			readFrom: readFrom,
		}
	}

	for _, src := range c.Sources.Journalctl {
		since := src.Since
		if since == "" {
			since = "now"
		}
		sourceConfigByID[src.ID] = sourceConfig{
			kind:  "journalctl",
			unit:  src.Unit,
			since: since,
		}
	}

	// Build parser config lookup map (using interface)
	parserConfigByID := make(map[string]parserconfig.Config)

	for _, p := range c.Parsers.JSON {
		parserConfigByID[p.ID] = parserconfig.JSONConfig{
			TimestampField:  withDefault(p.TimestampField, "timestamp"),
			TimestampFormat: p.TimestampFormat,
			LevelField:      withDefault(p.LevelField, "level"),
			TraceIDField:    withDefault(p.TraceIDField, "trace_id"),
			MessageField:    withDefault(p.MessageField, "message"),
		}
	}

	for _, p := range c.Parsers.Regex {
		parserConfigByID[p.ID] = parserconfig.RegexConfig{
			Pattern:         p.Pattern,
			TimestampField:  withDefault(p.TimestampField, "timestamp"),
			TimestampFormat: p.TimestampFormat,
			LevelField:      withDefault(p.LevelField, "level"),
			TraceIDField:    withDefault(p.TraceIDField, "trace_id"),
			MessageField:    withDefault(p.MessageField, "message"),
		}
	}

	for _, p := range c.Parsers.Text {
		parserConfigByID[p.ID] = parserconfig.TextConfig{
			TimestampRegex:  p.TimestampRegex,
			TimestampFormat: p.TimestampFormat,
			LevelRegex:      p.LevelRegex,
			TraceIDRegex:    p.TraceIDRegex,
		}
	}

	// Resolve apps to components
	processors := make(map[string]*processor.Processor)
	sources := make(map[string]source.Source)
	appNames := make([]string, 0, len(c.Apps))

	for _, app := range c.Apps {
		srcCfg, ok := sourceConfigByID[app.SourceRef]
		if !ok {
			return nil, fmt.Errorf("app %q references unknown source %q", app.Name, app.SourceRef)
		}

		parserCfg, ok := parserConfigByID[app.ParserRef]
		if !ok {
			return nil, fmt.Errorf("app %q references unknown parser %q", app.Name, app.ParserRef)
		}

		parser, err := parser.New(parserCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create parser for app %q: %w", app.Name, err)
		}

		var src source.Source
		switch srcCfg.kind {
		case "file":
			src = source.NewFile(source.FileConfig{
				AppName:  app.Name,
				Path:     srcCfg.path,
				ReadFrom: srcCfg.readFrom,
			})
		case "journalctl":
			src = source.NewJournalctl(source.JournalctlConfig{
				AppName: app.Name,
				Unit:    srcCfg.unit,
				Since:   srcCfg.since,
			})
		}

		procCfg := processor.Config{
			AppName:         app.Name,
			StartupPattern:  app.StartupPattern,
			ShutdownPattern: app.ShutdownPattern,
		}

		proc, err := processor.New(procCfg, parser)
		if err != nil {
			return nil, fmt.Errorf("failed to create processor for app %q: %w", app.Name, err)
		}

		processors[app.Name] = proc
		sources[app.Name] = src
		appNames = append(appNames, app.Name)
	}

	return &Resolved{
		Sources:    sources,
		Processors: processors,
		AppNames:   appNames,
	}, nil
}
