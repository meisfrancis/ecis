package ui

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/meisfrancis/ecis/internal/awsx"
	"github.com/meisfrancis/ecis/internal/config"
)

// Component is a screen that can be pushed onto the view stack.
type Component interface {
	tview.Primitive

	// Name is the stable identifier used for the page and the breadcrumb.
	Name() string
	// Title is the rendered heading, which may change as data loads.
	Title() string
	// Hints are the view's keyboard shortcuts, shown in the header menu.
	Hints() []Hint
	// Start begins any background refresh; Stop must halt it.
	Start()
	Stop()
}

// maxStackDepth bounds the view stack so a runaway drill-down cannot grow
// without limit.
const maxStackDepth = 12

// headerCostInterval is how often the header's month-to-date figure refreshes.
// Cost Explorer bills per request, so this is deliberately slow.
const headerCostInterval = time.Hour

// App is the ecis application shell: the header, prompt, view stack, crumbs and
// status bar, plus the AWS session everything runs against.
type App struct {
	*tview.Application

	cfg     *config.Config
	version string

	mu       sync.RWMutex
	client   *awsx.Client
	profile  string
	region   string
	cluster  string
	identity awsx.Identity

	root   *tview.Flex
	header *Header
	prompt *Prompt
	pages  *tview.Pages
	crumbs *Crumbs
	flash  *Flash

	stack        []Component
	promptActive bool
	headerHidden bool

	// CommandFn runs a command typed at the ":" prompt.
	CommandFn func(cmd string) error
	// SuggestFn supplies command autocompletion.
	SuggestFn func(prefix string) []string
	// HelpFn opens the help view.
	HelpFn func()

	// headerCPU and headerMem are the cluster-wide utilization gauges.
	headerCPU  float64
	headerMem  float64
	headerCost float64
	costLoaded bool
	tasksLine  string

	cancel context.CancelFunc
}

// NewApp builds the shell around an authenticated client.
func NewApp(cfg *config.Config, client *awsx.Client, version string) *App {
	a := &App{
		Application: tview.NewApplication(),
		cfg:         cfg,
		version:     version,
		client:      client,
		profile:     client.Profile,
		region:      client.Region,
		cluster:     cfg.Cluster,
		headerCPU:   math.NaN(),
		headerMem:   math.NaN(),
		headerCost:  math.NaN(),
	}

	a.header = NewHeader(version)
	a.prompt = NewPrompt()
	a.pages = tview.NewPages()
	a.crumbs = NewCrumbs()
	a.flash = NewFlash(func() { a.QueueUpdateDraw(func() {}) })

	a.pages.SetBackgroundColor(ColorBackground)

	a.root = tview.NewFlex().SetDirection(tview.FlexRow)
	a.root.SetBackgroundColor(ColorBackground)
	a.root.AddItem(a.header, HeaderRows, 0, false).
		AddItem(a.prompt, 0, 0, false).
		AddItem(a.pages, 0, 1, true).
		AddItem(a.crumbs, 1, 0, false).
		AddItem(a.flash, 1, 0, false)

	a.wirePrompt()
	a.SetRoot(a.root, true).EnableMouse(false)
	a.SetInputCapture(a.globalKeys)
	return a
}

// Config returns the active preferences.
func (a *App) Config() *config.Config { return a.cfg }

// Version returns the build version.
func (a *App) Version() string { return a.version }

// Flash returns the status bar.
func (a *App) Flash() *Flash { return a.flash }

// ReadOnly reports whether mutating actions are disabled.
func (a *App) ReadOnly() bool { return a.cfg.ReadOnly }

// Client returns the AWS client for the active context.
func (a *App) Client() *awsx.Client {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client
}

// Profile, Region and Cluster describe the active context.
func (a *App) Profile() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.profile
}

// Region returns the active region.
func (a *App) Region() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.region
}

// Cluster returns the active cluster, which scopes most views the way a
// namespace does in k9s.
func (a *App) Cluster() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cluster
}

// Identity returns the resolved caller identity.
func (a *App) Identity() awsx.Identity {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.identity
}

// SetIdentity records the caller identity for the header.
func (a *App) SetIdentity(id awsx.Identity) {
	a.mu.Lock()
	a.identity = id
	a.mu.Unlock()
}

// SetCluster switches the active cluster and invalidates the cached gauges so
// the header does not briefly show the previous cluster's numbers.
func (a *App) SetCluster(name string) {
	a.mu.Lock()
	changed := a.cluster != name
	a.cluster = name
	if changed {
		a.headerCPU, a.headerMem = math.NaN(), math.NaN()
		a.tasksLine = ""
	}
	a.mu.Unlock()
	a.RefreshHeader()
}

// SwitchContext rebuilds the AWS session for a new profile and/or region and
// resets the view stack, since nothing on screen belongs to the new context.
func (a *App) SwitchContext(ctx context.Context, profile, region string) error {
	client, err := awsx.New(ctx, profile, region)
	if err != nil {
		return err
	}
	id, idErr := client.Identity(ctx)

	a.mu.Lock()
	a.client, a.profile, a.region = client, client.Profile, client.Region
	a.identity = id
	a.cluster = ""
	a.headerCPU, a.headerMem, a.headerCost = math.NaN(), math.NaN(), math.NaN()
	a.costLoaded, a.tasksLine = false, ""
	a.mu.Unlock()

	if idErr != nil {
		a.flash.Warnf("switched context, but identity lookup failed: %v", idErr)
	}
	a.RefreshHeader()
	return nil
}

// Run starts the background refreshers and enters the event loop. It returns
// once the loop has exited and the views have been torn down.
func (a *App) Run(ctx context.Context) error {
	ctx, a.cancel = context.WithCancel(ctx)
	defer a.cancel()
	go a.refreshLoop(ctx)

	err := a.Application.Run()

	// tview drives input, drawing and queued updates on this goroutine, so the
	// view stack has a single owner. Once Run has returned nothing else is
	// touching it and the views can be stopped without synchronisation.
	for _, c := range a.stack {
		c.Stop()
	}
	return err
}

// Stop ends the session. It is safe to call from any goroutine — including a
// signal handler — because tview's Stop is lock-protected and the view teardown
// is left to Run, which owns the stack.
func (a *App) Stop() { a.Application.Stop() }

// refreshLoop keeps the header gauges current.
func (a *App) refreshLoop(ctx context.Context) {
	a.updateGauges(ctx)
	go a.updateCost(ctx)

	gauges := time.NewTicker(maxDuration(a.cfg.Interval(), 5*time.Second))
	cost := time.NewTicker(maxDuration(headerCostInterval, a.cfg.CostInterval()))
	defer gauges.Stop()
	defer cost.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-gauges.C:
			a.updateGauges(ctx)
		case <-cost.C:
			a.updateCost(ctx)
		}
	}
}

func (a *App) updateGauges(ctx context.Context) {
	cluster, client := a.Cluster(), a.Client()
	if cluster == "" || client == nil {
		a.QueueUpdateDraw(func() { a.RefreshHeader() })
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	samples, err := client.ClusterSamples(ctx, []string{cluster}, a.cfg.MetricPeriod())
	if err != nil {
		return
	}
	s := samples[cluster]

	clusters, cErr := client.Clusters(ctx)
	tasks := ""
	if cErr == nil {
		for _, c := range clusters {
			if c.Name != cluster {
				continue
			}
			tasks = fmt.Sprintf("%d running, %d pending, %d services",
				c.RunningTasks, c.PendingTasks, c.ActiveServices)
		}
	}

	a.mu.Lock()
	a.headerCPU, a.headerMem = s.CPU, s.Memory
	if tasks != "" {
		a.tasksLine = tasks
	}
	a.mu.Unlock()

	a.QueueUpdateDraw(func() { a.RefreshHeader() })
}

func (a *App) updateCost(ctx context.Context) {
	client := a.Client()
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	report, err := client.Cost(ctx, awsx.CostOptions{
		GroupBy:        awsx.GroupByTotal,
		Granularity:    "MONTHLY",
		Days:           1,
		ClusterTagKey:  a.cfg.Cost.ClusterTagKey,
		ServiceTagKey:  a.cfg.Cost.ServiceTagKey,
		IncludeCredits: a.cfg.Cost.IncludeCredits,
	})
	if err != nil {
		// Cost Explorer is commonly not enabled or not permitted; that should
		// dim the header field, not interrupt the session.
		a.mu.Lock()
		a.costLoaded, a.headerCost = true, math.NaN()
		a.mu.Unlock()
		a.QueueUpdateDraw(func() { a.RefreshHeader() })
		return
	}

	// The last bucket of a MONTHLY query is the current month to date.
	mtd := 0.0
	if len(report.Groups) > 0 {
		mtd = report.Groups[0].Latest()
	}
	a.mu.Lock()
	a.headerCost, a.costLoaded = mtd, true
	a.mu.Unlock()
	a.QueueUpdateDraw(func() { a.RefreshHeader() })
}

// RefreshHeader repaints the info panel, menu and crumbs from current state.
func (a *App) RefreshHeader() {
	a.mu.RLock()
	profile, region, cluster := a.profile, a.region, a.cluster
	id, cpu, mem, cost, loaded, tasks := a.identity, a.headerCPU, a.headerMem, a.headerCost, a.costLoaded, a.tasksLine
	a.mu.RUnlock()

	if cluster == "" {
		cluster = Colorize("all", ColorMuted)
	}
	account := id.Account
	if account == "" {
		account = Colorize("unknown", ColorMuted)
	} else if user := id.User(); user != "" {
		account = fmt.Sprintf("%s (%s)", account, Truncate(user, 16))
	}
	if tasks == "" {
		tasks = Colorize("-", ColorMuted)
	}

	costValue := Colorize("n/a", ColorMuted)
	switch {
	case !loaded:
		costValue = Colorize("loading…", ColorMuted)
	case !math.IsNaN(cost):
		costValue = Colorize(FormatMoney(cost, "USD")+" mtd", ColorCost)
	}

	a.header.SetInfo([]InfoField{
		{"Profile", profile},
		{"Region", region},
		{"Account", account},
		{"Cluster", cluster},
		{"Workload", tasks},
		{"CPU", fmt.Sprintf("%s %s", Gauge(cpu, 14), FormatPct(cpu))},
		{"MEM", fmt.Sprintf("%s %s", Gauge(mem, 14), FormatPct(mem))},
		{"Cost", costValue},
	})
	a.refreshHints()
	a.refreshCrumbs()
}

func (a *App) refreshHints() {
	var hints []Hint
	if top := a.Top(); top != nil {
		hints = append(hints, top.Hints()...)
	}
	hints = append(hints, GlobalHints()...)
	a.header.SetHints(hints)
}

func (a *App) refreshCrumbs() {
	crumbs := make([]string, 0, len(a.stack))
	for _, c := range a.stack {
		crumbs = append(crumbs, c.Name())
	}
	a.crumbs.SetCrumbs(crumbs)
}

// GlobalHints are the shortcuts available from every view.
func GlobalHints() []Hint {
	return []Hint{
		{":cmd", "Command"},
		{"/", "Filter"},
		{"?", "Help"},
		{"esc", "Back"},
		{"ctrl-a", "Aliases"},
		{"ctrl-r", "Reload"},
		{"ctrl-e", "Header"},
		{":q", "Quit"},
	}
}

// Top returns the component on top of the stack. Like the rest of the stack
// API, it must be called on the UI goroutine.
func (a *App) Top() Component {
	if len(a.stack) == 0 {
		return nil
	}
	return a.stack[len(a.stack)-1]
}

// StackDepth returns how many views are stacked.
func (a *App) StackDepth() int { return len(a.stack) }

// Push adds a view to the stack and gives it focus. The view underneath is
// stopped so background refreshes do not pile up behind the screen.
func (a *App) Push(c Component) {
	if len(a.stack) >= maxStackDepth {
		a.flash.Warn("view stack is full — press esc to go back")
		return
	}
	if top := a.Top(); top != nil {
		top.Stop()
	}
	a.stack = append(a.stack, c)
	name := a.pageName(len(a.stack)-1, c)
	a.pages.AddPage(name, c, true, true)
	c.Start()
	a.SetFocus(c)
	a.RefreshHeader()
}

// Pop removes the top view, restarting the one beneath it. The last view is
// never popped: something must always be on screen.
func (a *App) Pop() bool {
	if len(a.stack) <= 1 {
		return false
	}
	top := a.stack[len(a.stack)-1]
	top.Stop()
	a.pages.RemovePage(a.pageName(len(a.stack)-1, top))
	a.stack = a.stack[:len(a.stack)-1]

	next := a.Top()
	next.Start()
	a.SetFocus(next)
	a.RefreshHeader()
	return true
}

// Replace swaps the top view for another one at the same depth.
func (a *App) Replace(c Component) {
	if len(a.stack) == 0 {
		a.Push(c)
		return
	}
	top := a.stack[len(a.stack)-1]
	top.Stop()
	a.pages.RemovePage(a.pageName(len(a.stack)-1, top))

	a.stack[len(a.stack)-1] = c
	a.pages.AddPage(a.pageName(len(a.stack)-1, c), c, true, true)
	c.Start()
	a.SetFocus(c)
	a.RefreshHeader()
}

// Reset clears the stack down to a single root view.
func (a *App) Reset(c Component) {
	for i, comp := range a.stack {
		comp.Stop()
		a.pages.RemovePage(a.pageName(i, comp))
	}
	a.stack = nil
	a.Push(c)
}

func (a *App) pageName(depth int, c Component) string {
	return fmt.Sprintf("%d-%s", depth, c.Name())
}

// UpdateTitle refreshes the border title of the top view and the crumbs.
func (a *App) UpdateTitle() {
	a.RefreshHeader()
}

// wirePrompt connects the prompt bar to the command and filter handlers.
func (a *App) wirePrompt() {
	a.prompt.Suggest = func(prefix string) []string {
		if a.SuggestFn == nil {
			return nil
		}
		return a.SuggestFn(prefix)
	}
	a.prompt.OnChange = func(mode PromptMode, text string) {
		if mode != PromptFilter {
			return
		}
		if f, ok := a.Top().(Filterable); ok {
			f.SetFilter(text)
		}
	}
	a.prompt.OnAccept = func(mode PromptMode, text string) {
		a.hidePrompt()
		if mode == PromptFilter {
			return
		}
		if text == "" || a.CommandFn == nil {
			return
		}
		if err := a.CommandFn(text); err != nil {
			a.flash.Err(err)
		}
	}
	a.prompt.OnCancel = func(mode PromptMode) {
		if mode == PromptFilter {
			if f, ok := a.Top().(Filterable); ok {
				f.SetFilter("")
			}
		}
		a.hidePrompt()
	}
}

// Filterable is implemented by views that support the "/" filter.
type Filterable interface {
	SetFilter(string)
	Filter() string
}

// ShowPrompt opens the command or filter bar.
func (a *App) ShowPrompt(mode PromptMode) {
	seed := ""
	if mode == PromptFilter {
		if f, ok := a.Top().(Filterable); ok {
			seed = f.Filter()
		}
	}
	a.promptActive = true
	a.prompt.Activate(mode, seed)
	a.root.ResizeItem(a.prompt, 3, 0)
	a.SetFocus(a.prompt)
}

func (a *App) hidePrompt() {
	a.promptActive = false
	a.root.ResizeItem(a.prompt, 0, 0)
	if top := a.Top(); top != nil {
		a.SetFocus(top)
	}
}

// ToggleHeader hides or shows the header block, which is worth doing on a
// short terminal.
func (a *App) ToggleHeader() {
	a.headerHidden = !a.headerHidden
	if a.headerHidden {
		a.root.ResizeItem(a.header, 0, 0)
		return
	}
	a.root.ResizeItem(a.header, HeaderRows, 0)
}

// globalKeys handles the shortcuts that work from every view. Keys are passed
// through untouched whenever the prompt or a modal owns the keyboard.
func (a *App) globalKeys(evt *tcell.EventKey) *tcell.EventKey {
	if a.promptActive || a.modalOpen() {
		return evt
	}

	switch evt.Key() {
	case tcell.KeyEscape:
		if top, ok := a.Top().(Filterable); ok && top.Filter() != "" {
			top.SetFilter("")
			return nil
		}
		if a.Pop() {
			return nil
		}
		return nil
	case tcell.KeyCtrlA:
		if a.CommandFn != nil {
			_ = a.CommandFn("aliases")
		}
		return nil
	case tcell.KeyCtrlE:
		a.ToggleHeader()
		return nil
	case tcell.KeyCtrlR:
		if r, ok := a.Top().(Refreshable); ok {
			r.Refresh()
			a.flash.Info("refreshing…")
		}
		return nil
	}

	switch evt.Rune() {
	case ':':
		a.ShowPrompt(PromptCommand)
		return nil
	case '/':
		if _, ok := a.Top().(Filterable); ok {
			a.ShowPrompt(PromptFilter)
			return nil
		}
	case '?':
		if a.HelpFn != nil {
			a.HelpFn()
			return nil
		}
	}
	return evt
}

// Refreshable is implemented by views that can reload on demand.
type Refreshable interface {
	Refresh()
}

const modalPage = "modal"

func (a *App) modalOpen() bool { return a.pages.HasPage(modalPage) }

// Confirm shows a yes/no dialog and runs ok when the user confirms.
func (a *App) Confirm(title, message string, ok func()) {
	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Cancel", "Confirm"}).
		SetDoneFunc(func(_ int, label string) {
			a.dismissModal()
			if label == "Confirm" {
				ok()
			}
		})
	modal.SetBackgroundColor(ColorBackground).
		SetBorder(true).
		SetBorderColor(ColorWarn).
		SetTitle(" " + title + " ").
		SetTitleColor(ColorWarn)
	a.showModal(modal)
}

// Input shows a single-field dialog, calling ok with the entered value.
func (a *App) Input(title, label, initial string, ok func(string)) {
	field := tview.NewInputField().
		SetLabel(label + " ").
		SetText(initial).
		SetFieldWidth(24)
	field.SetFieldBackgroundColor(ColorSelected).
		SetFieldTextColor(ColorForeground).
		SetLabelColor(ColorKey)
	field.SetDoneFunc(func(key tcell.Key) {
		value := strings.TrimSpace(field.GetText())
		a.dismissModal()
		if key == tcell.KeyEnter {
			ok(value)
		}
	})

	form := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(field, 1, 0, true)
	form.SetBorder(true).
		SetBorderColor(ColorBorderFocus).
		SetTitle(" " + title + " ").
		SetTitleColor(ColorTitle).
		SetBackgroundColor(ColorBackground)

	a.showModal(center(form, maxInt(len(title), len(label)+30)+6, 3))
}

// showModal floats a primitive over the current view.
func (a *App) showModal(p tview.Primitive) {
	a.pages.AddPage(modalPage, p, true, true)
	a.SetFocus(p)
}

func (a *App) dismissModal() {
	a.pages.RemovePage(modalPage)
	if top := a.Top(); top != nil {
		a.SetFocus(top)
	}
}

// center wraps a primitive in flexes that keep it centred at a fixed size.
func center(p tview.Primitive, width, height int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(p, height+2, 0, true).
			AddItem(nil, 0, 1, false), width, 0, true).
		AddItem(nil, 0, 1, false)
}

// Shell suspends the TUI and runs an interactive command, which is how ECS Exec
// sessions get a real terminal.
func (a *App) Shell(bin string, args []string) {
	a.Suspend(func() {
		cmd := exec.Command(bin, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		fmt.Fprintf(os.Stdout, "\n%s %s\n\n", bin, strings.Join(args, " "))
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "\nsession ended: %v\n", err)
		}
		fmt.Fprint(os.Stdout, "\npress enter to return to ecis…")
		var scratch [1]byte
		_, _ = os.Stdin.Read(scratch[:])
	})
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// SetHealthy tints the logo: red once a refresh has failed, normal again once
// one succeeds. It mirrors the way k9s signals a broken connection.
func (a *App) SetHealthy(ok bool) { a.header.SetFailed(!ok) }

// Root returns the top-level layout primitive. It exists so the application can
// be rendered outside the event loop, which is what the smoke tests do.
func (a *App) Root() tview.Primitive { return a.root }
