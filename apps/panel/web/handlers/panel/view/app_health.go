package view

// AppHealthView carries the uptime text and bars for the app analytics page.
type AppHealthView struct {
	UptimeText      string
	UptimeTextClass string
	Bars            []UptimeBarView
}

// UptimeBarView is a single uptime bar in the uptime visualization.
type UptimeBarView struct {
	Class string
	Title string
}
