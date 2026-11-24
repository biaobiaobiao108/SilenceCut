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
	iconOpen    = editorIcon(`<path d="M3.5 7.5A2.5 2.5 0 0 1 6 5h3.8l2 2H18a2.5 2.5 0 0 1 2.5 2.5v1.2M4 10.7h16.7l-2.2 8H5.2z"/>`)
	iconSave    = editorIcon(`<path d="M5 4h11l3 3v13H5zM8 4v6h8V4M8 20v-6h8v6"/>`)
	iconSliders = editorIcon(`<path d="M4 7h16M4 17h16"/><circle cx="9" cy="7" r="2" fill="currentColor" stroke="none"/><circle cx="15" cy="17" r="2" fill="currentColor" stroke="none"/>`)
	iconFocus   = editorIcon(`<path d="M9 4H4v5M15 4h5v5M9 20H4v-5M15 20h5v-5M8 12h8"/>`)
)

func iconButton(c *ui.Context, icon *ui.SVG, label string, enabled, primary bool, action func()) ui.Element {
	button := flatAction(c, label, enabled, action).Width(controlHeight)
	button.Children(func() {
		ui.Icon(c, icon).Size(18, 18).TextColor(actionColor(enabled, primary))
	})
	return button
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
	button := flatAction(c, label, enabled, action).Padding(0, 9).TextColor(actionColor(enabled, false))
	button.Children(func() { ui.Text(c, label).FontSize(13).SingleLine() })
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
	button.Background(face).TextColor(textColor)
	button.Children(func() { ui.Text(c, label).FontSize(13).FontWeight(600).SingleLine() })
	return button
}

func actionFace(c *ui.Context, e ui.Element, enabled bool) {
	if !enabled {
		return
	}
	e.Cursor(ui.CursorPointer)
	face := ui.Transparent
	switch {
	case e.Pressed():
		face = pressedFace
	case e.Hovered():
		face = hoverFace
	}
	if c.Preferences().ReduceMotion {
		e.Background(face)
		return
	}
	e.Background(e.AnimateColor("face", face, ui.Animation{Duration: hoverDuration}))
	pose := ui.Pose{}
	if e.Pressed() {
		pose.Scale = .97
	}
	e.AnimatePose(pose, ui.Snappy)
}

func tabStrip(c *ui.Context, label string, selected *int, labels []string, fixedWidth bool, enabled bool, change func()) {
	tabs := ui.SegmentedBase(c, selected, len(labels))
	segmentWidth := float32(58)
	if !fixedWidth {
		segmentWidth = 0
		for _, text := range labels {
			w, _ := c.MeasureText(0, ui.Span{Text: text, Size: 12})
			segmentWidth = max(segmentWidth, w+22)
		}
	}
	at := float32(*selected)
	if !c.Preferences().ReduceMotion {
		at = tabs.Track.AnimateTo("selected", at, ui.Animation{Duration: switchDuration})
	}
	tabs.Track.Label(label).Width(segmentWidth*float32(len(labels)) + 4).Height(30).Padding(2).Radius(controlRadius).
		Disabled(!enabled).OnChange(change).Background(rail).Draw(func(p *ui.Painter, r ui.Rect) {
		if !enabled {
			return
		}
		capsule := ui.Rect{X: r.X + 2 + at*segmentWidth, Y: r.Y + 2, W: segmentWidth, H: r.H - 4}
		p.Fill(capsule, raised, controlRadius-2)
	}).Children(func() {
		for i, text := range labels {
			tab := tabs.Segment(i).Label(text).Width(segmentWidth).Height(26).
				Radius(controlRadius - 2).FocusRing(true).Disabled(!enabled)
			active := i == *selected
			tab.Children(func() {
				color := muted
				weight := 400
				if active {
					color = accent
					weight = 600
				} else if !enabled {
					color = disabledInk
				}
				ui.Text(c, text).FontSize(12).FontWeight(weight).TextColor(color).SingleLine()
			})
			if enabled && !active && tab.Hovered() {
				tab.Background(hoverFace.Alpha(.7))
			}
		}
	})
}
