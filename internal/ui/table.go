package ui

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Column describes one table column.
type Column struct {
	// Name is the header label; it is upper-cased on render.
	Name string
	// Align is a tview alignment constant.
	Align int
	// Wide hides the column unless wide mode is on (Ctrl-W).
	Wide bool
	// Numeric sorts by parsed number rather than lexically. Percentages,
	// currency and byte counts all parse.
	Numeric bool
	// Age sorts by parsed duration ("3d4h", "12m30s").
	Age bool
	// Expand gives the column a share of any spare width.
	Expand int
}

// Row is one table row. Ref carries the underlying domain object so drill-down
// and actions do not have to re-parse the rendered cells.
type Row struct {
	ID     string
	Cells  []string
	Colors []tcell.Color
	Ref    any
}

// Cell returns cell i, or the empty string when the row is short.
func (r Row) Cell(i int) string {
	if i < 0 || i >= len(r.Cells) {
		return ""
	}
	return r.Cells[i]
}

// Table is the sortable, filterable, markable table every resource view is
// built on. It keeps the full row set and derives the displayed set, so
// filtering and sorting never lose data and a background refresh can replace
// the contents without disturbing the cursor.
//
// Every method must be called on the UI goroutine: directly from a key handler,
// or inside a QueueUpdateDraw callback. A loader that fetches rows in the
// background hands them to Update from such a callback rather than calling it
// from its own goroutine.
type Table struct {
	*tview.Table

	columns  []Column
	rows     []Row
	view     []Row
	marks    map[string]bool
	filter   string
	filterRe *regexp.Regexp
	negate   bool
	sortCol  int
	sortDesc bool
	wide     bool

	// RowColor tints an entire row; per-cell colours in Row.Colors win.
	RowColor func(Row) tcell.Color
	// SelectionChanged fires when the cursor moves to a different row.
	SelectionChanged func(Row)
}

// NewTable returns an empty table wired with the standard key handling.
func NewTable(columns []Column) *Table {
	t := &Table{
		Table:   tview.NewTable(),
		columns: columns,
		marks:   map[string]bool{},
		sortCol: -1,
	}
	t.SetBorder(true).
		SetBorderColor(ColorBorder).
		SetTitleColor(ColorTitle).
		SetBackgroundColor(ColorBackground)
	t.SetSelectable(true, false).
		SetFixed(1, 0).
		SetSelectedStyle(tcell.StyleDefault.Background(ColorSelected).Foreground(ColorForeground).Bold(true))
	t.SetSelectionChangedFunc(func(row, _ int) {
		if t.SelectionChanged == nil {
			return
		}
		if r, ok := t.RowAt(row); ok {
			t.SelectionChanged(r)
		}
	})
	return t
}

// SetColumns replaces the column set, resetting any sort that pointed past the
// end of the new set.
func (t *Table) SetColumns(columns []Column) {
	t.columns = columns
	if t.sortCol >= len(columns) {
		t.sortCol = -1
	}
}

// Columns returns the current columns.
func (t *Table) Columns() []Column { return t.columns }

// Update replaces the row set and repaints, preserving both the selected row
// (by id) and the marks across the refresh.
func (t *Table) Update(rows []Row) {
	t.rows = rows
	t.Render()
}

// Rows returns the unfiltered row set.
func (t *Table) Rows() []Row { return t.rows }

// Count returns the number of rows currently displayed.
func (t *Table) Count() int { return len(t.view) }

// Total returns the number of rows before filtering.
func (t *Table) Total() int { return len(t.rows) }

// Render recomputes the displayed rows and repaints the widget.
func (t *Table) Render() {
	selectedID := t.selectedID()

	t.view = t.applyFilter(t.rows)
	t.applySort(t.view)

	t.Clear()
	t.renderHeader()
	for i, row := range t.view {
		t.renderRow(i+1, row)
	}

	t.restoreSelection(selectedID)
}

func (t *Table) visible() []int {
	out := make([]int, 0, len(t.columns))
	for i, c := range t.columns {
		if c.Wide && !t.wide {
			continue
		}
		out = append(out, i)
	}
	return out
}

func (t *Table) renderHeader() {
	col := 0
	for _, i := range t.visible() {
		c := t.columns[i]
		label := strings.ToUpper(c.Name)
		color := ColorHeader
		if i == t.sortCol {
			label += sortIndicator(t.sortDesc)
			color = ColorSorted
		}
		cell := tview.NewTableCell(label).
			SetTextColor(color).
			SetAlign(c.Align).
			SetSelectable(false).
			SetExpansion(c.Expand).
			SetAttributes(tcell.AttrBold)
		t.SetCell(0, col, cell)
		col++
	}
}

func sortIndicator(desc bool) string {
	if desc {
		return " ▼"
	}
	return " ▲"
}

func (t *Table) renderRow(rowIdx int, row Row) {
	base := ColorForeground
	if t.RowColor != nil {
		base = t.RowColor(row)
	}
	marked := t.marks[row.ID]

	col := 0
	for _, i := range t.visible() {
		c := t.columns[i]
		color := base
		if i < len(row.Colors) && row.Colors[i] != 0 {
			color = row.Colors[i]
		}
		text := row.Cell(i)
		if marked && col == 0 {
			text = "◉ " + text
		}
		cell := tview.NewTableCell(" " + text + " ").
			SetTextColor(color).
			SetAlign(c.Align).
			SetExpansion(c.Expand).
			SetReference(row.Ref)
		if marked {
			cell.SetBackgroundColor(ColorMarked)
		}
		t.SetCell(rowIdx, col, cell)
		col++
	}
}

// RowAt returns the row behind a screen row index (1-based, since row 0 is the
// header).
func (t *Table) RowAt(screenRow int) (Row, bool) {
	i := screenRow - 1
	if i < 0 || i >= len(t.view) {
		return Row{}, false
	}
	return t.view[i], true
}

// Selected returns the row under the cursor.
func (t *Table) Selected() (Row, bool) {
	r, _ := t.GetSelection()
	return t.RowAt(r)
}

func (t *Table) selectedID() string {
	if r, ok := t.Selected(); ok {
		return r.ID
	}
	return ""
}

// restoreSelection puts the cursor back on the same row after a repaint, or on
// the nearest valid row when it has gone away.
func (t *Table) restoreSelection(id string) {
	if len(t.view) == 0 {
		t.Select(0, 0)
		return
	}
	for i, r := range t.view {
		if r.ID == id {
			t.Select(i+1, 0)
			return
		}
	}
	row, _ := t.GetSelection()
	if row < 1 {
		row = 1
	}
	if row > len(t.view) {
		row = len(t.view)
	}
	t.Select(row, 0)
}

// SetFilter installs a row filter. A leading "!" negates it; the rest is used
// as a case-insensitive regular expression when it compiles, and as a plain
// substring otherwise, which keeps queries like "web-1.2" usable.
func (t *Table) SetFilter(q string) {
	t.filter, t.negate, t.filterRe = q, false, nil
	if strings.HasPrefix(q, "!") {
		t.negate, q = true, strings.TrimPrefix(q, "!")
	}
	if q != "" {
		if re, err := regexp.Compile("(?i)" + q); err == nil {
			t.filterRe = re
		}
	}
	t.Render()
}

// Filter returns the active filter expression.
func (t *Table) Filter() string { return t.filter }

// ClearFilter drops the filter.
func (t *Table) ClearFilter() {
	if t.filter == "" {
		return
	}
	t.SetFilter("")
}

func (t *Table) applyFilter(rows []Row) []Row {
	q := strings.TrimPrefix(t.filter, "!")
	if q == "" {
		out := make([]Row, len(rows))
		copy(out, rows)
		return out
	}
	lower := strings.ToLower(q)

	match := func(s string) bool {
		if t.filterRe != nil {
			return t.filterRe.MatchString(s)
		}
		return strings.Contains(strings.ToLower(s), lower)
	}

	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		// Match the whole rendered row so a query can span columns, and each
		// cell individually so an anchored pattern like "^web$" still works.
		hit := match(strings.Join(r.Cells, " "))
		for i := 0; !hit && i < len(r.Cells); i++ {
			hit = match(r.Cells[i])
		}
		if hit != t.negate {
			out = append(out, r)
		}
	}
	return out
}

// SortBy sorts on a column index, flipping direction when the same column is
// selected twice, exactly as k9s does.
func (t *Table) SortBy(col int) {
	if col < 0 || col >= len(t.columns) {
		return
	}
	if t.sortCol == col {
		t.sortDesc = !t.sortDesc
	} else {
		t.sortCol, t.sortDesc = col, false
		// Numbers are almost always more interesting largest-first.
		if t.columns[col].Numeric || t.columns[col].Age {
			t.sortDesc = true
		}
	}
	t.Render()
}

// SortByName sorts on the first column with a matching name.
func (t *Table) SortByName(name string) bool {
	for i, c := range t.columns {
		if strings.EqualFold(c.Name, name) {
			t.SortBy(i)
			return true
		}
	}
	return false
}

// ClearSort restores the natural order supplied by the data source.
func (t *Table) ClearSort() {
	t.sortCol, t.sortDesc = -1, false
	t.Render()
}

func (t *Table) applySort(rows []Row) {
	if t.sortCol < 0 || t.sortCol >= len(t.columns) {
		return
	}
	c := t.columns[t.sortCol]
	col := t.sortCol

	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].Cell(col), rows[j].Cell(col)
		var less bool
		if c.Numeric || c.Age {
			av, aok := sortValue(a, c.Age)
			bv, bok := sortValue(b, c.Age)
			switch {
			case aok && bok:
				less = av < bv
			case aok != bok:
				// Unparseable values ("-", "n/a") sort last in both directions.
				// Returning here bypasses the descending flip below, which is
				// what keeps them pinned to the bottom.
				return aok
			default:
				less = a < b
			}
		} else {
			less = strings.ToLower(a) < strings.ToLower(b)
		}
		if t.sortDesc {
			return !less
		}
		return less
	})
}

var durationRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(y|d|h|ms|m|s)`)

// durationUnits maps a duration suffix to seconds.
var durationUnits = map[string]float64{
	"y":  365 * 24 * 3600,
	"d":  24 * 3600,
	"h":  3600,
	"m":  60,
	"s":  1,
	"ms": 0.001,
}

// sortValue extracts a number from a rendered cell. It understands the shapes
// the tables actually produce: plain numbers, percentages, currency, ratios
// like "3/5" and compact durations like "2d4h".
func sortValue(s string, age bool) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" || s == "n/a" {
		return 0, false
	}
	if age {
		return parseCompactDuration(s)
	}
	// "running/desired" sorts by the first number.
	if i := strings.Index(s, "/"); i > 0 {
		s = s[:i]
	}
	clean := strings.TrimSpace(strings.NewReplacer("$", "", "%", "", ",", "", "USD", "").Replace(s))
	if v, err := strconv.ParseFloat(clean, 64); err == nil {
		return v, true
	}
	return parseCompactDuration(s)
}

func parseCompactDuration(s string) (float64, bool) {
	matches := durationRe.FindAllStringSubmatch(strings.ToLower(s), -1)
	if len(matches) == 0 {
		return 0, false
	}
	var total float64
	for _, m := range matches {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		total += v * durationUnits[m[2]]
	}
	return total, true
}

// ToggleWide shows or hides the wide columns.
func (t *Table) ToggleWide() {
	t.wide = !t.wide
	t.Render()
}

// Wide reports whether wide columns are shown.
func (t *Table) Wide() bool { return t.wide }

// ToggleMark marks or unmarks the selected row and advances the cursor, so
// marking a run of rows is a repeated keypress.
func (t *Table) ToggleMark() {
	row, ok := t.Selected()
	if !ok {
		return
	}
	if t.marks[row.ID] {
		delete(t.marks, row.ID)
	} else {
		t.marks[row.ID] = true
	}
	t.Render()

	r, _ := t.GetSelection()
	if r < len(t.view) {
		t.Select(r+1, 0)
	}
}

// ClearMarks unmarks everything.
func (t *Table) ClearMarks() {
	if len(t.marks) == 0 {
		return
	}
	t.marks = map[string]bool{}
	t.Render()
}

// Marked returns the marked rows, falling back to the selected row when
// nothing is marked. Actions use this so they work with or without marking.
func (t *Table) Marked() []Row {
	if len(t.marks) == 0 {
		if r, ok := t.Selected(); ok {
			return []Row{r}
		}
		return nil
	}
	out := make([]Row, 0, len(t.marks))
	for _, r := range t.view {
		if t.marks[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// MarkCount returns how many rows are marked.
func (t *Table) MarkCount() int { return len(t.marks) }

// Top moves the cursor to the first row.
func (t *Table) Top() { t.Select(1, 0) }

// Bottom moves the cursor to the last row.
func (t *Table) Bottom() {
	if len(t.view) > 0 {
		t.Select(len(t.view), 0)
	}
}

// TitleWith renders a k9s-style title: "Services(prod)[12]", with the filter
// appended when one is active.
func (t *Table) TitleWith(kind, scope string) string {
	count := fmt.Sprintf("[%d]", len(t.view))
	if len(t.view) != len(t.rows) {
		count = fmt.Sprintf("[%d/%d]", len(t.view), len(t.rows))
	}
	title := fmt.Sprintf(" [%s::b]%s[-::-]", Hex(ColorTitle), kind)
	if scope != "" {
		title += fmt.Sprintf("[%s](%s)[-]", Hex(ColorForeground), scope)
	}
	title += fmt.Sprintf("[%s]%s[-] ", Hex(ColorCPU), count)
	if t.filter != "" {
		title += fmt.Sprintf("[%s]</%s>[-] ", Hex(ColorWarn), t.filter)
	}
	return title
}

// SortHint renders the active sort for the crumbs line.
func (t *Table) SortHint() string {
	if t.sortCol < 0 || t.sortCol >= len(t.columns) {
		return ""
	}
	return strings.ToUpper(t.columns[t.sortCol].Name) + sortIndicator(t.sortDesc)
}

// FormatFloat renders a number with a sensible number of decimals for its size.
func FormatFloat(v float64) string {
	if math.IsNaN(v) {
		return "-"
	}
	switch {
	case v == 0:
		return "0"
	case math.Abs(v) >= 1000:
		return fmt.Sprintf("%.0f", v)
	case math.Abs(v) >= 1:
		return fmt.Sprintf("%.2f", v)
	default:
		return fmt.Sprintf("%.4f", v)
	}
}

// FormatMoney renders an amount with a currency prefix.
func FormatMoney(v float64, unit string) string {
	symbol := "$"
	if unit != "" && unit != "USD" {
		symbol = unit + " "
	}
	switch {
	case v == 0:
		return symbol + "0.00"
	case math.Abs(v) >= 1000:
		return fmt.Sprintf("%s%.0f", symbol, v)
	case math.Abs(v) >= 1:
		return fmt.Sprintf("%s%.2f", symbol, v)
	default:
		return fmt.Sprintf("%s%.4f", symbol, v)
	}
}
