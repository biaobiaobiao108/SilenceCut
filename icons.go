package main

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
)

//go:embed assets/silencecut.svg
var appIconSVG []byte

var appIcon = ui.MustParseSVG(appIconSVG)

func editorIcon(shape string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">` + shape + `</svg>`))
}

var (
	iconPlay    = editorIcon(`<path d="M8 4.8v14.4L19 12Z" fill="currentColor"/>`)
	iconPause   = editorIcon(`<rect x="7" y="5" width="3" height="14" rx=".75" fill="currentColor"/><rect x="14" y="5" width="3" height="14" rx=".75" fill="currentColor"/>`)
	iconStop    = editorIcon(`<rect x="6" y="6" width="12" height="12" rx="1.5" fill="currentColor"/>`)
	iconZoomOut = editorIcon(`<circle cx="10" cy="10" r="6"/><path d="m14.5 14.5 5 5M7.25 10h5.5"/>`)
	iconZoomIn  = editorIcon(`<circle cx="10" cy="10" r="6"/><path d="m14.5 14.5 5 5M7.25 10h5.5M10 7.25v5.5"/>`)
	iconKeep    = editorIcon(`<circle cx="12" cy="12" r="8"/><path d="m8.4 12 2.4 2.4 4.8-4.8"/>`)
	iconDiscard = editorIcon(`<circle cx="12" cy="12" r="8"/><path d="m6.3 6.3 11.4 11.4"/>`)
	iconInvert  = editorIcon(`<path d="M4 8h14m-3-3 3 3-3 3M20 16H6m3-3-3 3 3 3"/>`)
	iconUndo    = editorIcon(`<path d="M9 5 4 10l5 5M4 10h10a5 5 0 0 1 0 10h-2"/>`)
	iconRedo    = editorIcon(`<path d="m15 5 5 5-5 5m5-5H10a5 5 0 0 0 0 10h2"/>`)
	iconSample  = editorIcon(`<path d="M4 18a8 8 0 0 1 16 0M7 18h12M12 18l4-8"/><circle cx="12" cy="18" r="1.4" fill="currentColor" stroke="none"/>`)
	iconAudio   = editorIcon(`<path d="M3 12h2m3-4v8m4-12v16m4-13v10m4-6v2"/>`)
	iconVideo   = editorIcon(`<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M8 5v14m8-14v14M3 10h5m8 0h5M3 14h5m8 0h5"/>`)
	iconAdd     = editorIcon(`<path d="M12 5v14M5 12h14"/>`)
	iconRemove  = editorIcon(`<path d="M5 7h14m-9-3h4m-7 3 1 13h8l1-13M10 10v6m4-6v6"/>`)
)

func iconButton(c *ui.Context, icon *ui.SVG, label, hint string, enabled, primary bool, action func()) {
	flatAction(c, label, enabled, action).Width(controlHeight).Tooltip(hint).Children(func() {
		ui.Icon(c, icon).Size(19, 19).TextColor(actionColor(enabled, primary))
	})
}

func actionColor(enabled, primary bool) ui.Color {
	if !enabled {
		return disabledInk
	}
	if primary {
		return accent
	}
	return ink
}

func flatAction(c *ui.Context, label string, enabled bool, action func()) ui.Element {
	button := ui.ButtonBase(c).Role(ui.RoleButton).Label(label).Height(controlHeight).Radius(controlRadius).
		FocusRing(true).Disabled(!enabled).OnClick(action)
	actionFace(c, button, enabled)
	return button
}

func textAction(c *ui.Context, label string, enabled bool, action func()) ui.Element {
	button := flatAction(c, label, enabled, action).Padding(0, 7).TextColor(actionColor(enabled, false))
	button.Children(func() { ui.Text(c, label).FontSize(12).SingleLine() })
	return button
}

func primaryAction(c *ui.Context, label string, enabled bool, action func()) ui.Element {
	button := ui.ButtonBase(c).Role(ui.RoleButton).Label(label).Height(controlHeight).Radius(controlRadius).
		Padding(0, railInset).FocusRing(true).Disabled(!enabled).OnClick(action)
	face, textColor := rail, disabledInk
	if enabled {
		face, textColor = accent, worktop
		button.Cursor(ui.CursorPointer)
		switch {
		case button.Pressed():
			face = accentPressed
		case button.Hovered() && c.Preferences().ReduceMotion:
			face = accentHover
		default:
			hover := float32(0)
			if button.Hovered() {
				hover = 1
			}
			if c.Preferences().ReduceMotion {
				face = accent
			} else {
				face = accent.Mix(accentHover, button.Animate("hover", hover, hoverDuration))
			}
		}
	}
	button.Background(face).TextColor(textColor).Children(func() { ui.Text(c, label).SingleLine() })
	return button
}

func actionFace(c *ui.Context, e ui.Element, enabled bool) {
	if !enabled {
		return
	}
	e.Cursor(ui.CursorPointer)
	if e.Pressed() {
		e.Background(pressedFace)
	} else if e.Hovered() && c.Preferences().ReduceMotion {
		e.Background(hoverFace)
	} else {
		target := float32(0)
		if e.Hovered() {
			target = 1
		}
		if c.Preferences().ReduceMotion {
			e.Background(hoverFace.Alpha(0))
		} else {
			e.Background(hoverFace.Alpha(e.Animate("hover", target, hoverDuration)))
		}
	}
}

func tabStrip(c *ui.Context, label string, selected *int, labels []string, fixedWidth bool, enabled bool, change func()) {
	tabs := ui.SegmentedBase(c, selected, len(labels))
	tabs.Track.Label(label).Gap(5).Disabled(!enabled).OnChange(change).Children(func() {
		for i, text := range labels {
			tab := tabs.Segment(i).Label(text).Height(30).Padding(0, 6).
				Radius(controlRadius).FocusRing(true).Disabled(!enabled)
			if fixedWidth {
				tab.Width(58)
			}
			actionFace(c, tab, enabled)
			active := i == *selected
			tab.Children(func() {
				color := muted
				if active {
					color = actionColor(enabled, true)
				} else if !enabled {
					color = disabledInk
				}
				ui.Text(c, text).FontSize(12).TextColor(color).SingleLine()
			})
			if active {
				tab.Children(func() {
					indicator := ui.Box(c.Key("indicator")).Absolute().Left(6).Right(6).Bottom(0).Height(2).
						Background(actionColor(enabled, true))
					if !c.Preferences().ReduceMotion {
						indicator.Transition(ui.ElementTransition{Duration: switchDuration, Position: true, Width: true, Match: label})
					}
				})
			}
		}
	})
}
