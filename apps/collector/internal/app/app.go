package app


type Spec struct {
	Name            string `toml:"name"`
	SourceID        string `toml:"source_id"`
	ParserID        string `toml:"parser_id"`
}

type App struct {
        s *soruce.Source
        p parser.Parser
        Name string
}

func New(s *source.Source, p parser.Parser, name string) App {
        return App{
                s: s,
                p: p,
                Name: name
        }
}

func (a App) Produce() <- types.LogEntry {
        panic("not implemented")
}
