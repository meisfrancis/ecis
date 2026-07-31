package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/meisfrancis/ecis/internal/config"
)

// Details is the read-only text viewer behind describe and YAML views. It
// supports the same "/" search the tables use, wrapping, and saving to disk.
type Details struct {
	*tview.TextView

	app     *App
	name    string
	title   string
	subject string
	content string
	search  string
	wrap    bool
}

// NewDetails builds a text viewer. name is the breadcrumb entry, subject the
// resource being described.
func NewDetails(app *App, name, subject string) *Details {
	d := &Details{
		TextView: tview.NewTextView(),
		app:      app,
		name:     name,
		subject:  subject,
	}
	d.SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(false).
		SetBackgroundColor(ColorBackground)
	d.SetBorder(true).
		SetBorderColor(ColorBorder).
		SetTitleColor(ColorTitle)
	d.SetInputCapture(d.keys)
	d.updateTitle()
	return d
}

// SetContent replaces the displayed text.
func (d *Details) SetContent(text string) {
	d.content = text
	d.render()
}

// SetTitle overrides the rendered heading.
func (d *Details) SetTitleText(title string) {
	d.title = title
	d.updateTitle()
}

// Name implements Component.
func (d *Details) Name() string { return d.name }

// Title implements Component.
func (d *Details) Title() string {
	if d.title != "" {
		return d.title
	}
	return d.name
}

// Hints implements Component.
func (d *Details) Hints() []Hint {
	return []Hint{
		{"/", "Search"},
		{"w", "Wrap"},
		{"g", "Top"},
		{"G", "Bottom"},
		{"ctrl-s", "Save"},
	}
}

// Start implements Component; the viewer holds static text so there is nothing
// to start.
func (d *Details) Start() {}

// Stop implements Component.
func (d *Details) Stop() {}

// SetFilter implements Filterable, highlighting matches in the body.
func (d *Details) SetFilter(q string) {
	d.search = q
	d.render()
}

// Filter implements Filterable.
func (d *Details) Filter() string { return d.search }

func (d *Details) render() {
	text := tview.Escape(d.content)
	if d.search != "" {
		if re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(d.search)); err == nil {
			text = re.ReplaceAllStringFunc(text, func(m string) string {
				return fmt.Sprintf("[black:%s]%s[-:-]", Hex(ColorWarn), m)
			})
		}
	}
	d.SetText(text)
	d.updateTitle()
}

func (d *Details) updateTitle() {
	title := fmt.Sprintf(" [%s::b]%s[-::-]", Hex(ColorTitle), titleCase(d.name))
	if d.subject != "" {
		title += fmt.Sprintf("[%s](%s)[-]", Hex(ColorForeground), d.subject)
	}
	if d.search != "" {
		title += fmt.Sprintf(" [%s]</%s>[-]", Hex(ColorWarn), d.search)
	}
	d.SetTitle(title + " ")
}

func (d *Details) keys(evt *tcell.EventKey) *tcell.EventKey {
	switch evt.Key() {
	case tcell.KeyCtrlS:
		d.save()
		return nil
	}
	switch evt.Rune() {
	case 'w':
		d.wrap = !d.wrap
		d.SetWrap(d.wrap)
		d.app.Flash().Infof("wrap %s", onOff(d.wrap))
		return nil
	case 'g':
		d.ScrollToBeginning()
		return nil
	case 'G':
		d.ScrollToEnd()
		return nil
	}
	return evt
}

func (d *Details) save() {
	path, err := SaveDump(d.name+"-"+d.subject, d.content)
	if err != nil {
		d.app.Flash().Err(err)
		return
	}
	d.app.Flash().Infof("saved to %s", path)
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// SaveDump writes text to a timestamped file under the ecis config directory
// and returns the path.
func SaveDump(name, content string) (string, error) {
	dir := filepath.Join(config.Dir(), "dumps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create dump dir: %w", err)
	}
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)

	path := filepath.Join(dir, fmt.Sprintf("%s-%s.txt", safe, time.Now().Format("20060102-150405")))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write dump: %w", err)
	}
	return path, nil
}
