package ui

import (
	"fmt"
	"strings"

	"github.com/rivo/tview"
)

// HeaderRows is the height of the header block.
const HeaderRows = 8

// logoArt is the ecis banner.
var logoArt = []string{
	` ___   ___  ___  ___ `,
	`| __| / __||_ _|/ __|`,
	`| _| | (__  | | \__ \`,
	`|___| \___||___||___/`,
}

// InfoField is one key/value line in the header's info panel. Value may carry
// tview colour tags.
type InfoField struct {
	Key   string
	Value string
}

// Header is the top block: session info on the left, the keyboard menu in the
// middle, and the logo on the right.
type Header struct {
	*tview.Flex

	info *tview.TextView
	menu *Menu
	logo *tview.TextView

	version string
	failed  bool
}

// NewHeader builds the header.
func NewHeader(version string) *Header {
	h := &Header{
		Flex:    tview.NewFlex(),
		info:    tview.NewTextView(),
		menu:    NewMenu(),
		logo:    tview.NewTextView(),
		version: version,
	}
	h.info.SetDynamicColors(true).SetWrap(false).SetBackgroundColor(ColorBackground)
	h.logo.SetDynamicColors(true).SetWrap(false).SetTextAlign(tview.AlignLeft).SetBackgroundColor(ColorBackground)
	h.SetDirection(tview.FlexColumn).SetBackgroundColor(ColorBackground)

	h.AddItem(h.info, 40, 0, false).
		AddItem(h.menu, 0, 1, false).
		AddItem(h.logo, 23, 0, false)

	h.drawLogo()
	return h
}

// Menu returns the hint menu so views can install their own hints.
func (h *Header) Menu() *Menu { return h.menu }

// SetInfo replaces the info panel contents.
func (h *Header) SetInfo(fields []InfoField) {
	width := 0
	for _, f := range fields {
		if len(f.Key) > width {
			width = len(f.Key)
		}
	}
	var b strings.Builder
	for _, f := range fields {
		fmt.Fprintf(&b, "[%s::b]%s[-::-] %s\n",
			Hex(ColorKey), Pad(f.Key+":", width+1), f.Value)
	}
	h.info.SetText(b.String())
}

// SetHints installs the keyboard hints for the active view.
func (h *Header) SetHints(hints []Hint) { h.menu.SetHints(hints) }

// SetFailed tints the logo red, the way k9s signals a broken connection.
func (h *Header) SetFailed(failed bool) {
	if h.failed == failed {
		return
	}
	h.failed = failed
	h.drawLogo()
}

func (h *Header) drawLogo() {
	color := ColorLogo
	if h.failed {
		color = ColorError
	}
	var b strings.Builder
	for _, line := range logoArt {
		fmt.Fprintf(&b, "[%s::b]%s[-::-]\n", Hex(color), line)
	}
	fmt.Fprintf(&b, "[%s]ecs interactive shell[-]\n", Hex(ColorMuted))
	fmt.Fprintf(&b, "[%s]%s[-]", Hex(ColorMuted), h.version)
	h.logo.SetText(b.String())
}
