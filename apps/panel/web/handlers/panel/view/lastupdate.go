package view

import (
	"strconv"
	"time"
)

// LastUpdateView carries the stale/healthy indicator text and color shown next
// to a collector or app name.
type LastUpdateView struct {
	Text  string
	Class string
}

// FormatAgo renders a duration as a compact relative time ("1h 53m ago").
func FormatAgo(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	secs := int(age.Round(time.Second) / time.Second)
	switch {
	case secs < 60:
		return "last update " + strconv.Itoa(secs) + "s ago"
	case secs < 3600:
		return "last update " + strconv.Itoa(secs/60) + "m ago"
	case secs < 86400:
		return "last update " + strconv.Itoa(secs/3600) + "h " + strconv.Itoa((secs%3600)/60) + "m ago"
	default:
		return "last update " + strconv.Itoa(secs/86400) + "d " + strconv.Itoa((secs%86400)/3600) + "h ago"
	}
}

// LastUpdateClass colors the indicator by how long ago data arrived relative
// to the sampling interval: muted while within two intervals, yellow up to
// five, and red beyond that.
func LastUpdateClass(age, interval time.Duration) string {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	switch {
	case age <= 2*interval:
		return "text-fg-secondary"
	case age <= 5*interval:
		return "text-yellow-400"
	default:
		return "text-red-400"
	}
}
