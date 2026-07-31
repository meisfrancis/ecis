// Package ui holds the terminal widgets ecis is built from: the application
// shell, the sortable/filterable table every resource view reuses, the log
// viewer, and the chart primitives behind the monitoring and cost views.
package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

// Palette is the ecis colour scheme. It follows the k9s convention of a dark,
// mostly transparent background so the terminal's own theme shows through.
var (
	ColorBackground  = tcell.ColorDefault
	ColorForeground  = tcell.ColorWhite
	ColorBorder      = tcell.NewHexColor(0x3c5a78)
	ColorBorderFocus = tcell.NewHexColor(0x00b0ff)
	ColorTitle       = tcell.ColorAqua
	ColorLogo        = tcell.NewHexColor(0x00b0ff)
	ColorKey         = tcell.NewHexColor(0xffa500)
	ColorValue       = tcell.ColorWhite
	ColorHeader      = tcell.NewHexColor(0x87ceeb)
	ColorSorted      = tcell.ColorAqua
	ColorSelected    = tcell.NewHexColor(0x1c3f5e)
	ColorCrumbActive = tcell.ColorAqua
	ColorCrumb       = tcell.NewHexColor(0x2d4f6b)
	ColorMarked      = tcell.ColorDarkGoldenrod

	ColorOK      = tcell.NewHexColor(0x5fd75f)
	ColorWarn    = tcell.NewHexColor(0xffaf00)
	ColorError   = tcell.NewHexColor(0xff5f5f)
	ColorPending = tcell.NewHexColor(0x5fafff)
	ColorMuted   = tcell.NewHexColor(0x7a8894)
	ColorCPU     = tcell.NewHexColor(0x00d7ff)
	ColorMemory  = tcell.NewHexColor(0xd787ff)
	ColorCost    = tcell.NewHexColor(0x87d75f)
)

// Hex renders a colour as a tview colour tag body.
func Hex(c tcell.Color) string {
	r, g, b := c.RGB()
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// Colorize wraps text in a tview colour tag.
func Colorize(text string, c tcell.Color) string {
	return fmt.Sprintf("[%s]%s[-]", Hex(c), text)
}

// StatusColor maps an ECS status to a colour. ECS uses different vocabularies
// in different places (task last status, service status, deployment rollout
// state), so they are all folded into one lookup.
func StatusColor(status string) tcell.Color {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "RUNNING", "ACTIVE", "COMPLETED", "HEALTHY", "PRIMARY", "SUCCEEDED", "TRUE":
		return ColorOK
	case "PENDING", "PROVISIONING", "ACTIVATING", "IN_PROGRESS", "DRAINING", "DEPROVISIONING", "DEACTIVATING":
		return ColorPending
	case "STOPPED", "INACTIVE", "STOPPING", "FAILED", "UNHEALTHY", "FALSE":
		return ColorError
	case "UNKNOWN", "NONE", "":
		return ColorMuted
	default:
		return ColorForeground
	}
}

// LevelColor maps a utilization percentage to a colour: green while there is
// headroom, amber past 75%, red past 90%.
func LevelColor(pct float64) tcell.Color {
	switch {
	case pct < 0:
		return ColorMuted
	case pct >= 90:
		return ColorError
	case pct >= 75:
		return ColorWarn
	default:
		return ColorOK
	}
}

// StyleBox applies the standard border treatment to a widget.
func StyleBox(title string) (tcell.Color, tcell.Color) {
	return ColorBorder, ColorTitle
}

// Truncate shortens s to n runes, marking the cut with an ellipsis.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// Pad right-pads s with spaces to n runes.
func Pad(s string, n int) string {
	d := n - len([]rune(s))
	if d <= 0 {
		return s
	}
	return s + strings.Repeat(" ", d)
}

// titleCase upper-cases the first rune of s, for rendering view names as
// headings. strings.Title is deprecated and does more than is wanted here.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}
