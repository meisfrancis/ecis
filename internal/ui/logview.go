package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/meisfrancis/ecis/internal/awsx"
)

// logPollInterval is how often the viewer asks CloudWatch for new events.
const logPollInterval = 2 * time.Second

// sinceWindows are the ranges bound to the number keys, mirroring the way k9s
// binds 1-5 to log time windows.
var sinceWindows = []struct {
	Key   rune
	Label string
	Since time.Duration
}{
	{'0', "all", 14 * 24 * time.Hour},
	{'1', "1m", time.Minute},
	{'2', "5m", 5 * time.Minute},
	{'3', "15m", 15 * time.Minute},
	{'4', "1h", time.Hour},
	{'5', "24h", 24 * time.Hour},
}

// LogView tails CloudWatch Logs for a container, a task or a whole service.
type LogView struct {
	*tview.TextView

	app     *App
	name    string
	subject string
	group   string

	mu         sync.Mutex
	tail       *awsx.LogTail
	lines      []awsx.LogLine
	filter     string
	since      time.Duration
	autoscroll bool
	wrap       bool
	timestamps bool
	multi      bool
	full       bool
	err        error

	cancel context.CancelFunc
}

// NewLogView builds a log viewer over a tail. multi marks a tail that spans
// several streams, which turns on the per-line stream label.
func NewLogView(app *App, name, subject string, tail *awsx.LogTail, multi bool) *LogView {
	v := &LogView{
		TextView:   tview.NewTextView(),
		app:        app,
		name:       name,
		subject:    subject,
		tail:       tail,
		multi:      multi,
		since:      time.Duration(app.Config().LogSinceSeconds) * time.Second,
		autoscroll: true,
		timestamps: true,
	}
	if tail != nil {
		v.group = tail.Group
	}
	v.SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(false).
		SetMaxLines(app.Config().MaxLogLines).
		SetBackgroundColor(ColorBackground)
	v.SetBorder(true).
		SetBorderColor(ColorBorder).
		SetTitleColor(ColorTitle)
	v.SetInputCapture(v.keys)
	v.updateTitle()
	return v
}

// Name implements Component.
func (v *LogView) Name() string { return v.name }

// Title implements Component.
func (v *LogView) Title() string { return v.name }

// Hints implements Component.
func (v *LogView) Hints() []Hint {
	return []Hint{
		{"s", "Autoscroll"},
		{"w", "Wrap"},
		{"t", "Timestamps"},
		{"f", "Fullscreen"},
		{"c", "Clear"},
		{"/", "Filter"},
		{"0-5", "Since"},
		{"ctrl-s", "Save"},
		{"g/G", "Top/Bottom"},
	}
}

// Start begins tailing.
func (v *LogView) Start() {
	if v.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	go v.poll(ctx)
}

// Stop halts the tail.
func (v *LogView) Stop() {
	if v.cancel != nil {
		v.cancel()
		v.cancel = nil
	}
}

// Refresh implements Refreshable: rewinds and reloads the window.
func (v *LogView) Refresh() {
	v.mu.Lock()
	v.lines = nil
	if v.tail != nil {
		v.tail.Rewind(v.since)
	}
	v.mu.Unlock()
	v.render()
}

// SetFilter implements Filterable. Filtering is client-side over the buffered
// lines so it applies instantly and does not re-query CloudWatch.
func (v *LogView) SetFilter(q string) {
	v.mu.Lock()
	v.filter = q
	v.mu.Unlock()
	v.render()
}

// Filter implements Filterable.
func (v *LogView) Filter() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.filter
}

func (v *LogView) poll(ctx context.Context) {
	v.fetch(ctx)

	ticker := time.NewTicker(logPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			v.fetch(ctx)
		}
	}
}

func (v *LogView) fetch(ctx context.Context) {
	if v.tail == nil {
		return
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	lines, err := v.tail.Next(reqCtx, 1000)
	if ctx.Err() != nil {
		return
	}

	v.mu.Lock()
	v.err = err
	if len(lines) > 0 {
		v.lines = append(v.lines, lines...)
		if maxLines := v.app.Config().MaxLogLines; len(v.lines) > maxLines {
			v.lines = v.lines[len(v.lines)-maxLines:]
		}
	}
	v.mu.Unlock()

	if err != nil && !errors.Is(err, awsx.ErrNoLogGroup) {
		v.app.QueueUpdateDraw(func() { v.app.Flash().Err(err) })
	}
	v.app.QueueUpdateDraw(func() { v.renderLocked() })
}

func (v *LogView) render() {
	v.app.QueueUpdateDraw(func() { v.renderLocked() })
}

func (v *LogView) renderLocked() {
	v.mu.Lock()
	lines, filter, timestamps, multi, err := v.lines, v.filter, v.timestamps, v.multi, v.err
	v.mu.Unlock()

	var b strings.Builder
	needle := strings.ToLower(strings.TrimPrefix(filter, "!"))
	negate := strings.HasPrefix(filter, "!")

	shown := 0
	for _, line := range lines {
		if needle != "" {
			hit := strings.Contains(strings.ToLower(line.Message), needle)
			if hit == negate {
				continue
			}
		}
		if timestamps {
			fmt.Fprintf(&b, "[%s]%s[-] ", Hex(ColorMuted), line.Timestamp.Local().Format("15:04:05.000"))
		}
		if multi {
			fmt.Fprintf(&b, "[%s]%s[-] ", Hex(ColorPending), Truncate(shortStream(line.Stream), 24))
		}
		b.WriteString(highlight(tview.Escape(line.Message), needle))
		b.WriteByte('\n')
		shown++
	}

	if shown == 0 {
		switch {
		case errors.Is(err, awsx.ErrNoLogGroup):
			fmt.Fprintf(&b, "\n  [%s]No CloudWatch log group for this container.[-]\n", Hex(ColorWarn))
			fmt.Fprintf(&b, "  [%s]Only the awslogs driver can be tailed; check the task definition's logConfiguration.[-]\n", Hex(ColorMuted))
		case err != nil:
			fmt.Fprintf(&b, "\n  [%s]%s[-]\n", Hex(ColorError), tview.Escape(err.Error()))
		case filter != "":
			fmt.Fprintf(&b, "\n  [%s]No lines match %q.[-]\n", Hex(ColorMuted), filter)
		default:
			fmt.Fprintf(&b, "\n  [%s]Waiting for log events in %s…[-]\n", Hex(ColorMuted), v.group)
		}
	}

	v.SetText(b.String())
	v.updateTitle()
	if v.autoscroll {
		v.ScrollToEnd()
	}
}

func highlight(text, needle string) string {
	if needle == "" {
		return text
	}
	idx := strings.Index(strings.ToLower(text), needle)
	if idx < 0 {
		return text
	}
	return text[:idx] +
		fmt.Sprintf("[black:%s]%s[-:-]", Hex(ColorWarn), text[idx:idx+len(needle)]) +
		highlight(text[idx+len(needle):], needle)
}

// shortStream trims the awslogs prefix so the useful tail of the stream name
// (container and task id) fits in a narrow column.
func shortStream(s string) string {
	parts := strings.Split(s, "/")
	if len(parts) <= 2 {
		return s
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

func (v *LogView) updateTitle() {
	v.mu.Lock()
	count, filter, since := len(v.lines), v.filter, v.since
	v.mu.Unlock()

	title := fmt.Sprintf(" [%s::b]Logs[-::-][%s](%s)[-][%s][%d][-]",
		Hex(ColorTitle), Hex(ColorForeground), v.subject, Hex(ColorCPU), count)
	title += fmt.Sprintf(" [%s]since %s[-]", Hex(ColorMuted), awsx.Duration(since))
	if !v.autoscroll {
		title += fmt.Sprintf(" [%s]paused[-]", Hex(ColorWarn))
	}
	if filter != "" {
		title += fmt.Sprintf(" [%s]</%s>[-]", Hex(ColorWarn), filter)
	}
	v.SetTitle(title + " ")
}

func (v *LogView) keys(evt *tcell.EventKey) *tcell.EventKey {
	switch evt.Key() {
	case tcell.KeyCtrlS:
		v.save()
		return nil
	}

	switch r := evt.Rune(); r {
	case 's':
		v.autoscroll = !v.autoscroll
		v.app.Flash().Infof("autoscroll %s", onOff(v.autoscroll))
		v.renderLocked()
		return nil
	case 'w':
		v.wrap = !v.wrap
		v.SetWrap(v.wrap)
		v.app.Flash().Infof("wrap %s", onOff(v.wrap))
		return nil
	case 't':
		v.timestamps = !v.timestamps
		v.renderLocked()
		return nil
	case 'f':
		v.full = !v.full
		v.app.ToggleHeader()
		return nil
	case 'c':
		v.mu.Lock()
		v.lines = nil
		v.mu.Unlock()
		v.renderLocked()
		v.app.Flash().Info("cleared")
		return nil
	case 'g':
		v.autoscroll = false
		v.ScrollToBeginning()
		return nil
	case 'G':
		v.ScrollToEnd()
		return nil
	default:
		for _, w := range sinceWindows {
			if r != w.Key {
				continue
			}
			v.mu.Lock()
			v.since, v.lines = w.Since, nil
			if v.tail != nil {
				v.tail.Rewind(w.Since)
			}
			v.mu.Unlock()
			v.app.Flash().Infof("showing the last %s", w.Label)
			v.renderLocked()
			return nil
		}
	}
	return evt
}

func (v *LogView) save() {
	v.mu.Lock()
	var b strings.Builder
	for _, line := range v.lines {
		fmt.Fprintf(&b, "%s %s %s\n", line.Timestamp.Format(time.RFC3339Nano), line.Stream, line.Message)
	}
	v.mu.Unlock()

	path, err := SaveDump("logs-"+v.subject, b.String())
	if err != nil {
		v.app.Flash().Err(err)
		return
	}
	v.app.Flash().Infof("saved to %s", path)
}
