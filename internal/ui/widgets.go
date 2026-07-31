package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Hint is one entry in the keyboard menu.
type Hint struct {
	Key  string
	Desc string
}

// Menu renders the keyboard hints in the header, laid out in columns the way
// k9s does so the eye can scan down rather than across.
type Menu struct {
	*tview.TextView
	rows int
}

// NewMenu returns an empty menu.
func NewMenu() *Menu {
	m := &Menu{TextView: tview.NewTextView(), rows: 6}
	m.SetDynamicColors(true).
		SetWrap(false).
		SetBackgroundColor(ColorBackground)
	return m
}

// SetHints lays the hints out into columns of at most rows entries.
func (m *Menu) SetHints(hints []Hint) {
	if len(hints) == 0 {
		m.SetText("")
		return
	}
	cols := (len(hints) + m.rows - 1) / m.rows

	// Size each column to its widest entry so the columns line up.
	widths := make([]int, cols)
	for i, h := range hints {
		c := i / m.rows
		if w := len(h.Key) + len(h.Desc) + 4; w > widths[c] {
			widths[c] = w
		}
	}

	lines := make([]strings.Builder, m.rows)
	for i, h := range hints {
		row, col := i%m.rows, i/m.rows
		entry := fmt.Sprintf("[%s]<%s>[-] [%s]%s[-]", Hex(ColorKey), h.Key, Hex(ColorValue), h.Desc)
		lines[row].WriteString(entry)
		// Pad to the column width using the visible length, not the tagged one.
		pad := widths[col] - (len(h.Key) + len(h.Desc) + 4)
		lines[row].WriteString(strings.Repeat(" ", maxInt(pad, 0)+2))
	}

	var b strings.Builder
	for i := range lines {
		b.WriteString(strings.TrimRight(lines[i].String(), " "))
		b.WriteByte('\n')
	}
	m.SetText(b.String())
}

// Crumbs is the breadcrumb trail of the view stack.
type Crumbs struct {
	*tview.TextView
}

// NewCrumbs returns an empty breadcrumb bar.
func NewCrumbs() *Crumbs {
	c := &Crumbs{TextView: tview.NewTextView()}
	c.SetDynamicColors(true).
		SetWrap(false).
		SetBackgroundColor(ColorBackground)
	return c
}

// SetCrumbs renders the trail, highlighting the last (active) entry.
func (c *Crumbs) SetCrumbs(crumbs []string) {
	var b strings.Builder
	for i, crumb := range crumbs {
		bg, fg := ColorCrumb, ColorForeground
		if i == len(crumbs)-1 {
			bg, fg = ColorCrumbActive, tcell.ColorBlack
		}
		fmt.Fprintf(&b, "[%s:%s:b] %s [-:-:-] ", Hex(fg), Hex(bg), strings.ToLower(crumb))
	}
	c.SetText(b.String())
}

// FlashLevel is the severity of a status message.
type FlashLevel int

// Flash severities.
const (
	FlashInfo FlashLevel = iota
	FlashWarn
	FlashError
)

// flashTTL is how long a message stays on screen.
const flashTTL = 6 * time.Second

// Flash is the one-line status bar at the bottom of the screen.
type Flash struct {
	*tview.TextView

	mu      sync.Mutex
	timer   *time.Timer
	redraw  func()
	current string
}

// NewFlash returns an empty status bar. redraw is called when a message
// expires, so the bar can clear itself without a keystroke.
func NewFlash(redraw func()) *Flash {
	f := &Flash{TextView: tview.NewTextView(), redraw: redraw}
	f.SetDynamicColors(true).
		SetWrap(false).
		SetBackgroundColor(ColorBackground)
	return f
}

// Info shows an informational message.
func (f *Flash) Info(msg string) { f.show(FlashInfo, msg) }

// Warn shows a warning.
func (f *Flash) Warn(msg string) { f.show(FlashWarn, msg) }

// Err shows an error. A nil error clears the bar.
func (f *Flash) Err(err error) {
	if err == nil {
		f.Clear()
		return
	}
	f.show(FlashError, err.Error())
}

// Infof shows a formatted informational message.
func (f *Flash) Infof(format string, args ...any) { f.Info(fmt.Sprintf(format, args...)) }

// Warnf shows a formatted warning.
func (f *Flash) Warnf(format string, args ...any) { f.Warn(fmt.Sprintf(format, args...)) }

// Errf shows a formatted error.
func (f *Flash) Errf(format string, args ...any) { f.show(FlashError, fmt.Sprintf(format, args...)) }

// Clear empties the bar.
func (f *Flash) Clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopTimer()
	f.current = ""
	f.SetText("")
}

func (f *Flash) show(level FlashLevel, msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	icon, color := "🅸", ColorPending
	switch level {
	case FlashWarn:
		icon, color = "🆆", ColorWarn
	case FlashError:
		icon, color = "🅴", ColorError
	}
	f.current = msg
	// Errors are multi-line often enough (AWS wraps context) to be worth
	// flattening; the detail is still available in the log file.
	msg = strings.ReplaceAll(msg, "\n", " ")
	f.SetText(fmt.Sprintf("[%s]%s  %s[-]", Hex(color), icon, msg))

	f.stopTimer()
	f.timer = time.AfterFunc(flashTTL, func() {
		f.mu.Lock()
		f.SetText("")
		f.current = ""
		f.mu.Unlock()
		if f.redraw != nil {
			f.redraw()
		}
	})
}

func (f *Flash) stopTimer() {
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
}

// PromptMode is what the prompt bar is currently collecting.
type PromptMode int

// Prompt modes.
const (
	PromptCommand PromptMode = iota
	PromptFilter
)

// Prompt is the ":" command / "/" filter bar.
type Prompt struct {
	*tview.InputField

	mode PromptMode

	// OnAccept fires on Enter.
	OnAccept func(mode PromptMode, text string)
	// OnCancel fires on Esc.
	OnCancel func(mode PromptMode)
	// OnChange fires on every keystroke, which is what makes filtering live.
	OnChange func(mode PromptMode, text string)
	// Suggest supplies autocompletion entries in command mode.
	Suggest func(prefix string) []string
}

// NewPrompt returns a hidden prompt bar.
func NewPrompt() *Prompt {
	p := &Prompt{InputField: tview.NewInputField()}
	p.SetBorder(true).
		SetBorderColor(ColorBorderFocus).
		SetBackgroundColor(ColorBackground)
	p.SetFieldBackgroundColor(ColorBackground).
		SetFieldTextColor(ColorForeground).
		SetLabelColor(ColorKey).
		SetPlaceholderTextColor(ColorMuted)

	p.SetChangedFunc(func(text string) {
		if p.OnChange != nil {
			p.OnChange(p.mode, text)
		}
	})
	p.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEnter:
			if p.OnAccept != nil {
				p.OnAccept(p.mode, strings.TrimSpace(p.GetText()))
			}
		case tcell.KeyEscape:
			if p.OnCancel != nil {
				p.OnCancel(p.mode)
			}
		}
	})
	p.SetAutocompleteFunc(func(text string) []string {
		if p.mode != PromptCommand || p.Suggest == nil || text == "" {
			return nil
		}
		return p.Suggest(text)
	})
	return p
}

// Activate opens the prompt in a mode, seeded with text.
func (p *Prompt) Activate(mode PromptMode, text string) {
	p.mode = mode
	switch mode {
	case PromptFilter:
		p.SetLabel(" / ")
		p.SetPlaceholder("filter rows — regex or substring, prefix ! to invert")
	default:
		p.SetLabel(" > ")
		p.SetPlaceholder("resource, alias or command — try 'services', 'cost', 'help'")
	}
	p.SetText(text)
}

// Mode returns the active prompt mode.
func (p *Prompt) Mode() PromptMode { return p.mode }
