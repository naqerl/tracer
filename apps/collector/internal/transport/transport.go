package transport

type Spec struct {
	Name      string `toml:"name"`
	PanelHost string `toml:"panel_host"`
	APIKey    string `toml:"api_key"`
}
