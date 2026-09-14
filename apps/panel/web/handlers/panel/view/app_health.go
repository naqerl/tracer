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

// uptimeBarDelay delays bar i so one complete wave sweeps the whole row in a
// fixed total duration regardless of bar count.
func uptimeBarDelay(i, count int) float64 {
	step := 0.05
	if count > 1 {
		if s := 0.84 / float64(count); s < step {
			step = s
		}
	}
	return step * float64(i)
}
