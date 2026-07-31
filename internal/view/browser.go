// Package view implements the ecis resource views: the tables, the monitoring
// charts, the cost breakdowns, and the command registry that maps aliases like
// ":svc" onto them.
package view

import (
	"context"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/meisfrancis/ecis/internal/ui"
)

// ListFunc loads the rows of a browser.
type ListFunc func(ctx context.Context) ([]ui.Row, error)

// Browser is the generic resource table: it owns the refresh loop, the sort and
// filter keys, and the drill-down binding. Every listing view in ecis is a
// Browser with a different set of columns and a different loader.
type Browser struct {
	*ui.Table

	app   *ui.App
	name  string
	kind  string
	list  ListFunc
	hints []ui.Hint

	// Scope renders the parenthesised qualifier in the title, e.g. the cluster
	// a service list belongs to.
	Scope func() string
	// OnEnter drills into the selected row.
	OnEnter func(row ui.Row)
	// OnKey handles view-specific keys before the defaults.
	OnKey func(evt *tcell.EventKey) *tcell.EventKey
	// SortKeys maps a shifted rune to a column name.
	SortKeys map[rune]string
	// Interval overrides the configured refresh rate.
	Interval time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	loading bool
}

// NewBrowser builds a resource table. kind is the display name ("Services"),
// name the breadcrumb and command identifier ("services").
func NewBrowser(app *ui.App, name, kind string, columns []ui.Column, list ListFunc) *Browser {
	b := &Browser{
		Table: ui.NewTable(columns),
		app:   app,
		name:  name,
		kind:  kind,
		list:  list,
		SortKeys: map[rune]string{
			'N': "NAME",
			'A': "AGE",
			'S': "STATUS",
		},
	}
	b.SetInputCapture(b.keys)
	b.SetSelectedFunc(func(row, _ int) {
		if b.OnEnter == nil {
			return
		}
		if r, ok := b.RowAt(row); ok {
			b.OnEnter(r)
		}
	})
	b.SetTitle(b.Table.TitleWith(kind, ""))
	return b
}

// SetHints replaces the view's keyboard hints.
func (b *Browser) SetHints(hints []ui.Hint) { b.hints = hints }

// Name implements ui.Component.
func (b *Browser) Name() string { return b.name }

// Title implements ui.Component.
func (b *Browser) Title() string { return b.kind }

// Hints implements ui.Component.
func (b *Browser) Hints() []ui.Hint {
	out := append([]ui.Hint{}, b.hints...)
	if b.OnEnter != nil {
		out = append([]ui.Hint{{Key: "enter", Desc: "Drill down"}}, out...)
	}
	return append(out, ui.Hint{Key: "ctrl-w", Desc: "Wide"}, ui.Hint{Key: "space", Desc: "Mark"})
}

// Start begins the refresh loop.
func (b *Browser) Start() {
	b.mu.Lock()
	if b.cancel != nil {
		b.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.mu.Unlock()

	go b.loop(ctx)
}

// Stop halts the refresh loop.
func (b *Browser) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
}

// Refresh implements ui.Refreshable.
func (b *Browser) Refresh() {
	go b.fetch(context.Background())
}

func (b *Browser) loop(ctx context.Context) {
	b.fetch(ctx)

	interval := b.Interval
	if interval <= 0 {
		interval = b.app.Config().Interval()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.fetch(ctx)
		}
	}
}

// fetch reloads the rows. Overlapping fetches are dropped rather than queued:
// on a slow account a backlog would keep the table permanently stale.
func (b *Browser) fetch(ctx context.Context) {
	b.mu.Lock()
	if b.loading {
		b.mu.Unlock()
		return
	}
	b.loading = true
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		b.loading = false
		b.mu.Unlock()
	}()

	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	rows, err := b.list(reqCtx)
	if ctx.Err() != nil {
		return
	}

	b.app.QueueUpdateDraw(func() {
		if err != nil {
			b.app.SetHealthy(false)
			b.app.Flash().Err(err)
			return
		}
		b.app.SetHealthy(true)
		b.Update(rows)
		b.updateTitle()
	})
}

func (b *Browser) updateTitle() {
	scope := ""
	if b.Scope != nil {
		scope = b.Scope()
	}
	b.SetTitle(b.Table.TitleWith(b.kind, scope))
}

// SetFilter overrides ui.Table so the row count in the title tracks the filter.
func (b *Browser) SetFilter(q string) {
	b.Table.SetFilter(q)
	b.updateTitle()
}

func (b *Browser) keys(evt *tcell.EventKey) *tcell.EventKey {
	if b.OnKey != nil {
		if evt = b.OnKey(evt); evt == nil {
			return nil
		}
	}

	switch evt.Key() {
	case tcell.KeyCtrlW:
		b.ToggleWide()
		b.app.Flash().Infof("wide columns %s", onOff(b.Wide()))
		return nil
	case tcell.KeyCtrlBackslash:
		b.ClearMarks()
		return nil
	}

	switch r := evt.Rune(); r {
	case ' ':
		b.ToggleMark()
		return nil
	default:
		if name, ok := b.SortKeys[r]; ok {
			if b.SortByName(name) {
				b.app.Flash().Infof("sorted by %s", name)
				b.updateTitle()
				return nil
			}
		}
	}
	return evt
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// Column shorthands, so the view definitions stay readable.

// col is a plain left-aligned column.
func col(name string) ui.Column { return ui.Column{Name: name, Align: tview.AlignLeft} }

// colW is a column shown only in wide mode.
func colW(name string) ui.Column {
	return ui.Column{Name: name, Align: tview.AlignLeft, Wide: true}
}

// colN is a right-aligned numeric column.
func colN(name string) ui.Column {
	return ui.Column{Name: name, Align: tview.AlignRight, Numeric: true}
}

// colA is a right-aligned age column, sorted by parsed duration.
func colA(name string) ui.Column {
	return ui.Column{Name: name, Align: tview.AlignRight, Age: true}
}

// colE is a left-aligned column that absorbs spare width.
func colE(name string) ui.Column {
	return ui.Column{Name: name, Align: tview.AlignLeft, Expand: 1}
}
