package view

import (
	"fmt"
	"math"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/meisfrancis/ecis/internal/ui"
)

// rowBuilder assembles a table row cell by cell, tracking per-cell colours so
// the views can highlight a status or a utilization level without juggling
// parallel slices.
type rowBuilder struct {
	cells  []string
	colors []tcell.Color
}

// add appends a plain cell. The text is escaped: ECS carries plenty of
// free-form strings (stopped reasons, event messages, image tags) that would
// otherwise be mangled by tview's colour tag syntax.
func (b *rowBuilder) add(text string) *rowBuilder {
	return b.addRaw(tview.Escape(text))
}

// addRaw appends a cell whose text may contain tview colour tags. Use it only
// for content ecis generates itself, such as sparklines and bars.
func (b *rowBuilder) addRaw(text string) *rowBuilder {
	b.cells = append(b.cells, text)
	b.colors = append(b.colors, 0)
	return b
}

// addf appends a formatted plain cell.
func (b *rowBuilder) addf(format string, args ...any) *rowBuilder {
	return b.add(fmt.Sprintf(format, args...))
}

// addC appends a coloured cell.
func (b *rowBuilder) addC(text string, color tcell.Color) *rowBuilder {
	b.cells = append(b.cells, tview.Escape(text))
	b.colors = append(b.colors, color)
	return b
}

// addStatus appends a cell coloured by ECS status.
func (b *rowBuilder) addStatus(status string) *rowBuilder {
	return b.addC(dash(status), ui.StatusColor(status))
}

// addPct appends a utilization cell coloured by level.
func (b *rowBuilder) addPct(v float64) *rowBuilder {
	if math.IsNaN(v) {
		return b.addC("-", ui.ColorMuted)
	}
	return b.addC(ui.FormatPct(v), ui.LevelColor(v))
}

// addInt appends a numeric cell.
func (b *rowBuilder) addInt(v int) *rowBuilder { return b.addf("%d", v) }

// addMoney appends a currency cell.
func (b *rowBuilder) addMoney(v float64, unit string) *rowBuilder {
	return b.addC(ui.FormatMoney(v, unit), ui.ColorCost)
}

// build finishes the row.
func (b *rowBuilder) build(id string, ref any) ui.Row {
	return ui.Row{ID: id, Cells: b.cells, Colors: b.colors, Ref: ref}
}

// dash renders an empty value as a dash so columns never look truncated.
func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// boolText renders a boolean as a status-coloured word.
func boolText(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// ratio renders a running/desired pair.
func ratio(a, b int) string { return fmt.Sprintf("%d/%d", a, b) }

// ratioColor colours a running/desired pair: green when matched, amber while
// converging, red when nothing is running but something should be.
func ratioColor(running, desired int) tcell.Color {
	switch {
	case desired == 0 && running == 0:
		return ui.ColorMuted
	case running == desired:
		return ui.ColorOK
	case running == 0:
		return ui.ColorError
	default:
		return ui.ColorWarn
	}
}

// trendText renders a percentage change with an arrow.
func trendText(pct float64) (string, tcell.Color) {
	switch {
	case math.IsNaN(pct) || pct == 0:
		return "→ 0%", ui.ColorMuted
	case pct > 0:
		return fmt.Sprintf("↑ %.0f%%", pct), ui.ColorWarn
	default:
		return fmt.Sprintf("↓ %.0f%%", -pct), ui.ColorOK
	}
}
