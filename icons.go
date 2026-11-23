package main

import "github.com/egoist/mygo/ui"

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
	iconSplit   = editorIcon(`<circle cx="6.2" cy="6.2" r="2.5"/><circle cx="6.2" cy="17.8" r="2.5"/><path d="m8 8 12 12M8 16 20 4"/>`)
	iconUndo    = editorIcon(`<path d="M9 5 4 10l5 5M4 10h10a5 5 0 0 1 0 10h-2"/>`)
	iconRedo    = editorIcon(`<path d="m15 5 5 5-5 5m5-5H10a5 5 0 0 0 0 10h2"/>`)
	iconSample  = editorIcon(`<path d="m13 7 4 4m-3-5 2-2a2.1 2.1 0 0 1 3 3l-2 2 .8.8-2 2-.8-.8-7.6 7.6-4 .9.9-4L12 8l-.8-.8 2-2Z"/>`)
	iconAudio   = editorIcon(`<path d="M3 12h2m3-4v8m4-12v16m4-13v10m4-6v2"/>`)
	iconVideo   = editorIcon(`<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M8 5v14m8-14v14M3 10h5m8 0h5M3 14h5m8 0h5"/>`)
	iconAdd     = editorIcon(`<path d="M12 5v14M5 12h14"/>`)
	iconRemove  = editorIcon(`<path d="M5 7h14m-9-3h4m-7 3 1 13h8l1-13M10 10v6m4-6v6"/>`)
)

func iconButton(c *ui.Context, icon *ui.SVG, label, hint string, enabled, primary bool, action func()) {
	button := ui.Button
	if primary {
		button = ui.PrimaryButton
	}
	button(c, "").Size(34, 34).Padding(0).Label(label).Tooltip(hint).Disabled(!enabled).
		OnClick(action).Children(func() {
		ui.Icon(c, icon).Size(18, 18)
	})
}
