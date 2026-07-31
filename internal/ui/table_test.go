package ui

import (
	"math"
	"testing"

	"github.com/rivo/tview"
)

func TestSortValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		age  bool
		want float64
		ok   bool
	}{
		{"plain integer", "42", false, 42, true},
		{"percentage", "87.5%", false, 87.5, true},
		{"currency", "$1,234.50", false, 1234.50, true},
		{"ratio takes the first number", "3/5", false, 3, true},
		{"empty is unsortable", "", false, 0, false},
		{"dash is unsortable", "-", false, 0, false},
		{"n/a is unsortable", "n/a", false, 0, false},
		{"seconds", "45s", true, 45, true},
		{"minutes and seconds", "12m30s", true, 750, true},
		{"hours and minutes", "2h05m", true, 7500, true},
		{"days and hours", "3d04h", true, 3*86400 + 4*3600, true},
		{"years and days", "1y10d", true, 365*86400 + 10*86400, true},
		{"non-duration text", "RUNNING", true, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := sortValue(tc.in, tc.age)
			if ok != tc.ok {
				t.Fatalf("sortValue(%q, %v) ok = %v, want %v", tc.in, tc.age, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("sortValue(%q, %v) = %v, want %v", tc.in, tc.age, got, tc.want)
			}
		})
	}
}

func newTestTable() *Table {
	return NewTable([]Column{
		{Name: "NAME"},
		{Name: "CPU", Numeric: true},
		{Name: "AGE", Age: true},
	})
}

func testRows() []Row {
	return []Row{
		{ID: "b", Cells: []string{"beta", "50%", "2h"}},
		{ID: "a", Cells: []string{"alpha", "5%", "3d"}},
		{ID: "c", Cells: []string{"gamma", "-", "30s"}},
	}
}

func TestSortByNumericDescendsFirst(t *testing.T) {
	tbl := newTestTable()
	tbl.Update(testRows())

	// Numeric columns start descending: the busiest row is the interesting one.
	tbl.SortByName("CPU")
	if got := tbl.view[0].ID; got != "b" {
		t.Errorf("first row after CPU sort = %q, want %q", got, "b")
	}
	// Unsortable values sort last regardless of direction.
	if got := tbl.view[2].ID; got != "c" {
		t.Errorf("unsortable row placed at %v, want last", tbl.view)
	}

	tbl.SortByName("CPU")
	if got := tbl.view[0].ID; got != "a" {
		t.Errorf("first row after reversing CPU sort = %q, want %q", got, "a")
	}
	if got := tbl.view[2].ID; got != "c" {
		t.Errorf("unsortable row moved on reverse: %v", tbl.view)
	}
}

func TestSortByAgeParsesDurations(t *testing.T) {
	tbl := newTestTable()
	tbl.Update(testRows())

	tbl.SortByName("AGE")
	want := []string{"a", "b", "c"} // 3d, 2h, 30s
	for i, id := range want {
		if tbl.view[i].ID != id {
			t.Fatalf("age sort = %v, want order %v", ids(tbl.view), want)
		}
	}
}

func TestFilterSubstringAndNegation(t *testing.T) {
	tbl := newTestTable()
	tbl.Update(testRows())

	tbl.SetFilter("alpha")
	if got := tbl.Count(); got != 1 {
		t.Fatalf("filter %q matched %d rows, want 1", "alpha", got)
	}

	tbl.SetFilter("!alpha")
	if got := tbl.Count(); got != 2 {
		t.Fatalf("negated filter matched %d rows, want 2", got)
	}

	// A regular expression is used when the pattern compiles.
	tbl.SetFilter("^(beta|gamma)$")
	if got := tbl.Count(); got != 2 {
		t.Fatalf("regex filter matched %d rows, want 2", got)
	}

	tbl.ClearFilter()
	if got := tbl.Count(); got != 3 {
		t.Fatalf("cleared filter left %d rows, want 3", got)
	}
}

func TestFilterFallsBackToSubstringOnBadRegex(t *testing.T) {
	tbl := newTestTable()
	tbl.Update([]Row{{ID: "a", Cells: []string{"web-1.2(canary", "1%", "1h"}}})

	// "(" alone is not a valid regex; it must still match literally.
	tbl.SetFilter("(canary")
	if got := tbl.Count(); got != 1 {
		t.Fatalf("literal filter matched %d rows, want 1", got)
	}
}

func TestUpdatePreservesSelectionByID(t *testing.T) {
	tbl := newTestTable()
	tbl.Update(testRows())
	tbl.Select(2, 0) // the "alpha" row

	selected, ok := tbl.Selected()
	if !ok || selected.ID != "a" {
		t.Fatalf("selected row = %+v, want id a", selected)
	}

	// Reordering the data must not move the cursor to a different resource.
	tbl.Update([]Row{
		{ID: "c", Cells: []string{"gamma", "-", "30s"}},
		{ID: "a", Cells: []string{"alpha", "9%", "3d"}},
		{ID: "b", Cells: []string{"beta", "50%", "2h"}},
	})

	selected, ok = tbl.Selected()
	if !ok || selected.ID != "a" {
		t.Errorf("after update selected = %+v, want id a", selected)
	}
}

func TestUpdateClampsSelectionWhenRowVanishes(t *testing.T) {
	tbl := newTestTable()
	tbl.Update(testRows())
	tbl.Select(3, 0)

	tbl.Update(testRows()[:1])
	row, ok := tbl.Selected()
	if !ok {
		t.Fatal("no row selected after the table shrank")
	}
	if row.ID != "b" {
		t.Errorf("selected %q, want the only remaining row %q", row.ID, "b")
	}
}

func TestMarkedFallsBackToSelection(t *testing.T) {
	tbl := newTestTable()
	tbl.Update(testRows())
	tbl.Select(1, 0)

	marked := tbl.Marked()
	if len(marked) != 1 || marked[0].ID != "b" {
		t.Fatalf("Marked() with no marks = %v, want the selected row", ids(marked))
	}

	tbl.ToggleMark() // marks "b" and advances
	tbl.ToggleMark() // marks "a"
	marked = tbl.Marked()
	if len(marked) != 2 {
		t.Fatalf("Marked() = %v, want 2 rows", ids(marked))
	}

	tbl.ClearMarks()
	if tbl.MarkCount() != 0 {
		t.Errorf("MarkCount() = %d after clearing, want 0", tbl.MarkCount())
	}
}

func TestWideColumnsHiddenByDefault(t *testing.T) {
	tbl := NewTable([]Column{{Name: "NAME"}, {Name: "ARN", Wide: true}})
	if got := len(tbl.visible()); got != 1 {
		t.Fatalf("visible columns = %d, want 1 with wide mode off", got)
	}
	tbl.ToggleWide()
	if got := len(tbl.visible()); got != 2 {
		t.Fatalf("visible columns = %d, want 2 with wide mode on", got)
	}
}

func TestTitleReportsFilteredCount(t *testing.T) {
	tbl := newTestTable()
	tbl.Update(testRows())

	if got := tbl.TitleWith("Services", "prod"); !contains(got, "[3]") {
		t.Errorf("unfiltered title %q should carry the row count", got)
	}
	tbl.SetFilter("alpha")
	title := tbl.TitleWith("Services", "prod")
	if !contains(title, "[1/3]") {
		t.Errorf("filtered title %q should carry both counts", title)
	}
	if !contains(title, "alpha") {
		t.Errorf("filtered title %q should show the filter", title)
	}
}

func TestFormatMoneyScalesPrecision(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "$0.00"},
		{0.0042, "$0.0042"},
		{12.5, "$12.50"},
		{4210.7, "$4211"},
	}
	for _, tc := range tests {
		if got := FormatMoney(tc.in, "USD"); got != tc.want {
			t.Errorf("FormatMoney(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatPctHandlesMissingData(t *testing.T) {
	if got := FormatPct(math.NaN()); got != "-" {
		t.Errorf("FormatPct(NaN) = %q, want %q", got, "-")
	}
	if got := FormatPct(42.26); got != "42.3%" {
		t.Errorf("FormatPct(42.26) = %q, want %q", got, "42.3%")
	}
}

// TestCellTextIsEscaped guards the rendering path against ECS strings that
// contain tview colour tag syntax, such as a stopped reason with brackets.
func TestCellTextIsEscaped(t *testing.T) {
	escaped := tview.Escape("Task failed [ELB]")
	if escaped == "Task failed [ELB]" {
		t.Fatal("tview.Escape left the bracket sequence untouched")
	}
}

func ids(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
