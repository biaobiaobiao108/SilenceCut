package main

import (
	"math"
	"strings"

	"github.com/egoist/mygo/ui"

	"silencecut/internal/timeline"
)

func (a *app) drawWave(p *ui.Painter, r ui.Rect, item *asset, length int64) {
	span := timeline.Span{Start: a.viewStart, End: a.viewStart + length}
	body := ui.Rect{X: r.X, Y: r.Y + rulerHeight, W: r.W, H: r.H - rulerHeight}
	clips := item.history.Clips
	mid := body.Y + body.H/2
	first := clipAt(clips, span.Start)
	var removed ui.Path
	for i := first; i < len(clips) && clips[i].Start < span.End; i++ {
		clip := clips[i]
		left, right := timeX(body, span, max(clip.Start, span.Start)), timeX(body, span, min(clip.End, span.End))
		color := waveRemove
		if clip.Keep {
			color = waveKeep
		} else {
			y := body.Y + body.H - 3
			removed.MoveTo(left, y).LineTo(right, y)
		}
		p.Fill(ui.Rect{X: left, Y: body.Y, W: right - left, H: body.H}, color, 0)
	}
	drawTimeRuler(p, r, span)
	p.Line(body.X, mid, body.X+body.W, mid, 1, border)
	columns := item.waveform.Columns(span, item.info.Rate, max(1, int(body.W)), a.waveColumns)
	a.waveColumns = columns
	drawEnvelope(p, body, columns, item.waveform.Amplitude, func(x float32) ui.Color {
		at := span.Start + int64(float64(x-body.X)/float64(body.W)*float64(length))
		for first < len(clips) && clips[first].End <= at {
			first++
		}
		if first < len(clips) && !clips[first].Keep {
			return waveRemovedInk
		}
		return waveKeptInk
	})
	p.StrokePath(removed.Cap(ui.ButtCap).Dash(0, 4, 3), 1, waveSelection.Alpha(.55))
	for i := clipAt(clips, span.Start); i+1 < len(clips) && clips[i].End < span.End; i++ {
		x := timeX(body, span, clips[i].End)
		p.Line(x, body.Y, x, body.Y+body.H, 1, waveSelection.Alpha(.25))
		p.Fill(ui.Rect{X: x - 1, Y: body.Y + 2, W: 2, H: 6}, waveSelection.Alpha(.72), 1)
	}
	selected, ok := a.timelineSelection(item)
	if drag := a.waveDrag; drag.moved && (drag.kind == waveLocate || drag.kind >= waveSelectStart && drag.kind <= waveSelectMove) {
		selected, ok = drag.draft, drag.draft.End > drag.draft.Start
	}
	if ok {
		drawWaveSelection(p, body, span, selected)
	} else if i := clipAt(clips, min(a.position, item.info.Duration-1)); i < len(clips) {
		clip := clips[i]
		if clip.End > span.Start && clip.Start < span.End {
			left := timeX(body, span, max(clip.Start, span.Start))
			right := timeX(body, span, min(clip.End, span.End))
			p.Stroke(ui.Rect{X: left, Y: body.Y, W: right - left, H: body.H}, waveSelection.Alpha(.35), 0, 1)
		}
	}
	if a.waveDrag.kind == waveBoundary && a.waveDrag.moved {
		x := playheadX(body, span, a.waveDrag.end)
		p.Line(x, body.Y, x, body.Y+body.H, 2, waveSelection)
		labelX := max(r.X+4, min(x+6, r.X+r.W-92))
		p.Fill(ui.Rect{X: labelX - 3, Y: r.Y + 1, W: 88, H: rulerHeight - 2}, waveBack, 0)
		p.Text(labelX, r.Y+1, clock(a.waveDrag.end), 11, waveSelection)
	}
	drawPlayhead(p, r, span, a.position, true)
}

func drawWaveSelection(p *ui.Painter, body ui.Rect, span, selection timeline.Span) {
	if selection.End <= span.Start || selection.Start >= span.End {
		return
	}
	left := timeX(body, span, max(selection.Start, span.Start))
	right := timeX(body, span, min(selection.End, span.End))
	p.Fill(ui.Rect{X: left, Y: body.Y, W: right - left, H: body.H}, waveSelection.Alpha(.14), 0)
	p.Line(left, body.Y+1, right, body.Y+1, 1.5, waveSelection)
	p.Line(left, body.Y+body.H-1, right, body.Y+body.H-1, 1.5, waveSelection)
	for i, edge := range [...]int64{selection.Start, selection.End} {
		if edge < span.Start || edge > span.End {
			continue
		}
		x := playheadX(body, span, edge)
		p.Line(x, body.Y, x, body.Y+body.H, 1.5, waveSelection)
		grip := selectionGripX(body, span, selection, i == 0)
		p.Fill(ui.Rect{X: grip - selectionGrip/2, Y: body.Y + body.H/2 - 8, W: selectionGrip, H: 16}, waveSelection, 1)
	}
}

func drawTimeRuler(p *ui.Painter, r ui.Rect, span timeline.Span) {
	target := float64(span.End-span.Start) * 76 / float64(max(1, r.W))
	power := math.Pow(10, math.Floor(math.Log10(max(1, target))))
	step := int64(power * 10)
	for _, multiple := range []float64{1, 2, 5} {
		if multiple*power >= target {
			step = max(1, int64(multiple*power))
			break
		}
	}
	y := r.Y + rulerHeight
	p.Line(r.X, y-1, r.X+r.W, y-1, 1, border)
	for at := (span.Start + step - 1) / step * step; at < span.End; at += step {
		x := timeX(r, span, at)
		p.Line(x+.5, y, x+.5, r.Y+r.H, 1, waveSelection.Alpha(.08))
		p.Line(x+.5, y-6, x+.5, y, 1, muted)
		if x+64 < r.X+r.W {
			label := clock(at)
			if step >= timeline.Second {
				label = strings.SplitN(label, ".", 2)[0]
			} else if step >= 100_000 {
				label = label[:len(label)-2]
			}
			p.Text(x+4, r.Y+1, label, 11, muted)
		}
	}
}

func drawEnvelope(p *ui.Painter, r ui.Rect, columns []timeline.Peak, amplitude float32, color func(float32) ui.Color) {
	if amplitude <= 0 {
		return
	}
	width := r.W / float32(len(columns))
	// Keep an audio-only track from exaggerating peaks when the editor is tall.
	mid, half := r.Y+r.H/2, max(1, min(r.H/2-9, min(165, r.W*.21)))
	for i, peak := range columns {
		if peak.Min == 0 && peak.Max == 0 {
			continue
		}
		x := r.X + float32(i)*width
		top := mid - envelopeHeight(float32(peak.Max)/amplitude)*half
		bottom := mid + envelopeHeight(-float32(peak.Min)/amplitude)*half
		p.Fill(ui.Rect{X: x, Y: top, W: width, H: max(.65, bottom-top)}, color(x+width/2), 0)
	}
}

func envelopeHeight(level float32) float32 {
	const knee = .78
	if level <= knee {
		return level
	}
	loud := level - knee
	return knee + (1-knee)*loud/(loud+1-knee)
}

func drawPlayhead(p *ui.Painter, r ui.Rect, span timeline.Span, at int64, marker bool) {
	if at < span.Start || at > span.End {
		return
	}
	x := playheadX(r, span, at)
	p.Line(x, r.Y, x, r.Y+r.H, 2, accent)
	if marker {
		var head ui.Path
		head.MoveTo(x-5, r.Y).LineTo(x+5, r.Y).LineTo(x+5, r.Y+6).
			LineTo(x, r.Y+11).LineTo(x-5, r.Y+6).Close()
		p.FillPath(&head, accent)
	}
}

func playheadX(r ui.Rect, span timeline.Span, at int64) float32 {
	return max(r.X+1, min(r.X+r.W-1, timeX(r, span, at)))
}

func (a *app) overview(c *ui.Context, item *asset, duration int64, allowed bool) {
	var overviewHandle ui.Handle
	e := ui.Box(c.Key("overview")).Height(28).Shrink(0).Radius(controlRadius).Background(waveBack).
		BorderColor(border).BorderWidth(1).Clip().Focusable().FocusRing(true).
		Label("全局波形").Description("点击定位，拖动窗口浏览").Bind(&overviewHandle)
	length := a.detailLength(duration)
	if allowed && !a.overviewDrag.active {
		pan := func(delta int64) {
			a.viewStart = max(0, min(duration-length, a.viewStart+delta))
			a.scheduleRecovery()
		}
		e.OnShortcut(0, ui.KeyLeft, func() { pan(-length / 4) })
		e.OnShortcut(0, ui.KeyRight, func() { pan(length / 4) })
	}
	x, _, over := e.PointerPosition()
	w := e.Bounds().W
	e.HandleInput(func(ev ui.InputEvent) bool {
		if a.selected != item || ev.Button != 0 {
			return false
		}
		w := overviewHandle.Bounds(c).W
		length := a.detailLength(duration)
		switch ev.Kind {
		case ui.InputPointerDown:
			if a.busy() || w <= 0 || a.waveDrag.kind != waveIdle || a.overviewDrag.active {
				return false
			}
			a.stopPlayback()
			at := waveTime(ev.X, w, duration)
			offset := length / 2
			if at >= a.viewStart && at <= a.viewStart+length {
				offset = at - a.viewStart
			}
			a.overviewDrag = overviewGesture{active: true, x: ev.X, viewStart: a.viewStart, offset: offset}
			return true
		case ui.InputPointerMove:
			if a.overviewDrag.active && !a.busy() && w > 0 {
				return a.trackOverview(ev.X, waveTime(ev.X, w, duration), duration, length)
			}
		case ui.InputPointerUp:
			if !a.overviewDrag.active {
				return false
			}
			if ev.Canceled || a.busy() || w <= 0 {
				a.cancelOverview()
			} else {
				a.finishOverview(ev.X, w, duration, length)
			}
			return true
		}
		return false
	})
	if !allowed && a.overviewDrag.active {
		a.cancelOverview()
	} else if allowed && w > 0 && (over || a.overviewDrag.active) && a.waveDrag.kind == waveIdle {
		at := waveTime(x, w, duration)
		switch {
		case length < duration && a.overviewDrag.moved:
			e.Cursor(ui.CursorGrabbing)
		case length < duration && at >= a.viewStart && at <= a.viewStart+length:
			e.Cursor(ui.CursorGrab)
		default:
			e.Cursor(ui.CursorCrosshair)
		}
	}
	e.Draw(func(p *ui.Painter, r ui.Rect) {
		span := timeline.Span{End: duration}
		left, right := timeX(r, span, a.viewStart), timeX(r, span, a.viewStart+length)
		window := ui.Rect{X: left, Y: r.Y + 1, W: right - left, H: r.H - 2}
		p.Fill(window, overviewWindow, 2)
		columns := item.waveform.Columns(span, item.info.Rate, max(1, int(r.W)), a.overviewColumns)
		a.overviewColumns = columns
		drawEnvelope(p, r, columns, item.waveform.Amplitude, func(float32) ui.Color { return waveOverview })
		drawPlayhead(p, r, span, a.position, false)
		p.Stroke(window, waveSelection, 2, 1)
	})
}

func (a *app) trackOverview(x float32, at, duration, length int64) bool {
	drag := &a.overviewDrag
	if !drag.active {
		return false
	}
	if math.Abs(float64(x-drag.x)) >= waveDragSlop {
		drag.moved = true
	}
	if drag.moved {
		a.viewStart = max(0, min(duration-length, at-drag.offset))
	}
	return drag.moved
}

func (a *app) finishOverview(x, w float32, duration, length int64) {
	at := waveTime(x, w, duration)
	if !a.trackOverview(x, at, duration, length) {
		if at < a.viewStart || at > a.viewStart+length {
			a.viewStart = max(0, min(duration-length, at-length/2))
		}
		a.seek(at)
	}
	a.overviewDrag = overviewGesture{}
	a.scheduleRecovery()
}

func (a *app) cancelOverview() {
	a.viewStart = a.overviewDrag.viewStart
	a.overviewDrag = overviewGesture{}
	a.scheduleRecovery()
}
