package main

import (
	"math"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"silencecut/internal/timeline"
)

const (
	detailDefault = 30 * timeline.Second
	detailMinimum = 2 * timeline.Second
)

var (
	waveKeptInk    = ui.Hex("#2A5549")
	waveRemovedInk = ui.Hex("#8E4439")
	waveOverview   = ui.Hex("#586C78")
	overviewWindow = ui.Hex("#D8E9EE")
)

type waveGesture struct {
	active, boundary, leftKeep bool
	at, end, min, max          int64
	x                          float32
}

type overviewGesture struct {
	active, pendingClick bool
	offset, at           int64
	x                    float32
}

func (a *app) detailLength(duration int64) int64 {
	if a.viewLength == 0 {
		return min(duration, detailDefault)
	}
	return min(duration, a.viewLength)
}

func (a *app) followPlayhead() {
	duration := a.selected.info.Duration
	length := a.detailLength(duration)
	if a.position < a.viewStart || a.position > a.viewStart+length {
		a.viewStart = min(a.position, duration-length)
	}
}

func (a *app) setZoom(length int64) {
	duration := a.selected.info.Duration
	old := a.detailLength(duration)
	length = max(min(duration, detailMinimum), min(duration, length))
	anchor := a.position
	if anchor < a.viewStart || anchor > a.viewStart+old {
		anchor = a.viewStart + old/2
	}
	fraction := float64(anchor-a.viewStart) / float64(old)
	a.viewLength = length
	a.viewStart = max(0, min(duration-length, anchor-int64(fraction*float64(length))))
}

func clipAt(clips []timeline.Clip, at int64) int {
	return sort.Search(len(clips), func(i int) bool { return clips[i].End > at })
}

func (a *app) stopPlayback() {
	if a.player != nil {
		a.player.Pause()
		a.wasPlaying = false
	}
}

func (a *app) timeline(c *ui.Context) {
	item := a.selected
	var duration, kept int64
	var undo, redo, current bool
	if item != nil {
		duration = item.info.Duration
		for _, clip := range item.history.Clips {
			if clip.Keep {
				kept += clip.End - clip.Start
			}
		}
		undo, redo = item.history.CanUndo(), item.history.CanRedo()
		_, current = a.editSpan()
	}
	ready := duration > 0
	allowed := ready && !a.busy()
	playable := a.previewPlayable(kept)
	length := a.detailLength(duration)
	minLength := min(duration, detailMinimum)
	zoom := 0.0
	if duration > minLength {
		zoom = math.Log(float64(duration)/float64(length)) / math.Log(float64(duration)/float64(minLength))
	}
	width, height := c.Size()
	transport := func() {
		ui.Row(c.Key("transport")).AlignItems(ui.Center).Gap(5).Children(func() {
			icon, label := iconPlay, "播放"
			if a.player != nil && a.player.Playing() {
				icon, label = iconPause, "暂停"
			}
			iconButton(c.Key("play"), icon, label, label+"（空格）", playable, true, a.togglePlay)
			iconButton(c.Key("stop"), iconStop, "停止", "停止并回到开头", allowed && a.player != nil, false, func() {
				a.stopPlayback()
				a.viewStart = 0
				a.seek(0)
			})
			mode := 0
			if a.originalPreview {
				mode = 1
			}
			ui.Segmented(c.Key("preview-mode"), &mode, "过滤后", "原始").Label("试听模式").FontSize(12).
				Disabled(!allowed).OnChange(func() {
				a.setPreviewMode(mode == 1)
			})
		})
	}
	tools := func() {
		ui.Row(c.Key("edit-tools")).AlignItems(ui.Center).Gap(3).Children(func() {
			iconButton(c.Key("zoom-out"), iconZoomOut, "缩小波形", "缩小波形", ready && length < duration, false,
				func() { a.setZoom(length * 3 / 2) })
			ui.Slider(c.Key("zoom"), &zoom, 0, 1).Width(86).Label("波形缩放").Disabled(duration <= minLength).
				OnChange(func() {
					a.setZoom(int64(math.Round(float64(duration) *
						math.Pow(float64(minLength)/float64(duration), zoom))))
				})
			iconButton(c.Key("zoom-in"), iconZoomIn, "放大波形", "放大波形", ready && length > minLength, false,
				func() { a.setZoom(length * 2 / 3) })
			ui.Box(c).Size(1, 20).Background(border).Margin(0, 6)
			editable := allowed && current
			iconButton(c.Key("keep"), iconKeep, "保留", "保留选区或当前片段", editable, false, func() { a.edit(true) })
			iconButton(c.Key("discard"), iconDiscard, "移除", "移除选区或当前片段", editable, false, func() { a.edit(false) })
			iconButton(c.Key("split"), iconSplit, "分割", "在游标处标记 0.2 秒的反向片段",
				editable && a.position < duration, false, a.splitAtPlayhead)
			ui.Box(c).Size(1, 20).Background(border).Margin(0, 6)
			iconButton(c.Key("undo"), iconUndo, "撤销", "撤销", allowed && undo, false, func() { a.history(false) })
			iconButton(c.Key("redo"), iconRedo, "重做", "重做", allowed && redo, false, func() { a.history(true) })
			iconButton(c.Key("sample-threshold"), iconSample, "采集游标处音量", "采集游标处音量作为自定义阈值",
				allowed && len(item.peaks) > 0, false, a.sampleThreshold)
		})
	}
	pane(ui.Column(c)).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
			transport()
			if width >= 1040 {
				ui.Box(c).Size(1, 20).Background(border)
				tools()
			}
			ui.Spacer(c)
			a.timelineReadout(c, duration, kept)
		})
		if width < 1040 {
			ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
				tools()
				ui.Spacer(c)
				if current {
					span, _ := a.editSpan()
					ui.Text(c, clock(span.Start)+" – "+clock(span.End)).FontFeatures("tnum").
						TextColor(muted).FontSize(11)
				}
			})
		}
		waveHeight := min(182, max(127, height*.195))
		if ready && !item.info.Video {
			waveHeight = min(270, max(waveHeight, 135+(height-690)*.72))
		}
		a.wave(c, item, duration, waveHeight, allowed)
		a.overview(c, item, duration, allowed)
	})
}

func (a *app) timelineReadout(c *ui.Context, duration, kept int64) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, clock(a.position)).FontFeatures("tnum").FontSize(15).Bold()
		ui.Text(c, "/ "+clock(duration)).FontFeatures("tnum").FontSize(12).TextColor(muted)
		if duration > 0 {
			label, background, color := "保留 "+clock(kept), waveKeep, waveKeptInk
			if kept == 0 {
				label, background, color = "无保留片段", waveRemove, waveRemovedInk
			}
			ui.Text(c, label).FontFeatures("tnum").FontSize(12).
				Background(background).TextColor(color).Radius(4).Padding(4, 7)
		}
	})
}

func (a *app) splitAtPlayhead() {
	clips := a.selected.history.Clips
	i := clipAt(clips, a.position)
	if i < len(clips) {
		a.stopPlayback()
		a.selected.history.Edit(timeline.Span{
			Start: a.position, End: min(clips[i].End, a.position+200_000),
		}, !clips[i].Keep)
		a.selection = timeline.Span{}
	}
}

func (a *app) wave(c *ui.Context, item *asset, duration int64, height float32, allowed bool) {
	e := ui.Box(c.Key("wave")).Height(height).Radius(3).Background(waveBack).Clip().
		Focusable().Label("音频波形和播放位置")
	if duration == 0 {
		e.Children(func() { ui.Text(c, "").Height(height) })
		return
	}
	if allowed {
		nudge := func(delta int64) {
			a.selection = timeline.Span{}
			a.stopPlayback()
			a.seek(a.position + delta)
			a.followPlayhead()
		}
		e.OnShortcut(0, ui.KeyLeft, func() { nudge(-100_000) })
		e.OnShortcut(0, ui.KeyRight, func() { nudge(100_000) })
	}
	length := a.detailLength(duration)
	x, y, over := e.PointerPosition()
	w := e.Bounds().W
	var at int64
	boundary := -1
	if allowed && w > 0 {
		at = a.viewStart + int64(float64(max(0, min(x/w, 1)))*float64(length))
		e.Cursor(ui.CursorCrosshair)
		if over && y >= rulerHeight && y < rulerHeight+13 {
			boundary = nearestBoundary(item.history.Clips, at, int64(float64(length)*6/float64(w)))
		}
		if boundary >= 0 || a.waveDrag.boundary {
			e.Cursor(ui.CursorResizeEW)
		}
	}
	_, _, pressed := e.Dragged()
	if pressed && allowed && w > 0 {
		drag := &a.waveDrag
		switch {
		case !drag.active:
			a.selection = timeline.Span{}
			a.stopPlayback()
			*drag = waveGesture{active: true, at: at, end: at, x: x}
			if boundary >= 0 {
				left, right := item.history.Clips[boundary], item.history.Clips[boundary+1]
				drag.boundary, drag.leftKeep = true, left.Keep
				drag.at, drag.end, drag.min, drag.max = left.End, left.End, left.Start, right.End
			}
			a.seek(drag.at)
		case drag.boundary:
			drag.end = max(drag.min, min(drag.max, at))
		case math.Abs(float64(x-drag.x)) >= 3 || a.selection.End > a.selection.Start:
			a.selection = timeline.Span{Start: min(drag.at, at), End: max(drag.at, at)}
		}
	} else if drag := a.waveDrag; drag.active {
		a.waveDrag = waveGesture{}
		if allowed && drag.boundary && drag.end != drag.at {
			item.history.Edit(timeline.Span{Start: min(drag.at, drag.end), End: max(drag.at, drag.end)},
				(drag.end > drag.at) == drag.leftKeep)
			a.seek(drag.end)
		}
	}
	e.Draw(func(p *ui.Painter, r ui.Rect) { a.drawWave(p, r, item, length) })
}

func nearestBoundary(clips []timeline.Clip, at, tolerance int64) int {
	i := clipAt(clips, at)
	if i > 0 && (i >= len(clips)-1 || at-clips[i-1].End <= clips[i].End-at) {
		i--
	}
	if i+1 >= len(clips) || clips[i].End < at-tolerance || clips[i].End > at+tolerance {
		return -1
	}
	return i
}

const rulerHeight = 22

func timeX(r ui.Rect, span timeline.Span, at int64) float32 {
	return r.X + r.W*float32(float64(at-span.Start)/float64(span.End-span.Start))
}

func (a *app) drawWave(p *ui.Painter, r ui.Rect, item *asset, length int64) {
	span := timeline.Span{Start: a.viewStart, End: a.viewStart + length}
	body := ui.Rect{X: r.X, Y: r.Y + rulerHeight, W: r.W, H: r.H - rulerHeight}
	clips := item.history.Clips
	mid := body.Y + body.H/2
	first := clipAt(clips, span.Start)
	for i := first; i < len(clips) && clips[i].Start < span.End; i++ {
		clip := clips[i]
		left, right := timeX(body, span, max(clip.Start, span.Start)), timeX(body, span, min(clip.End, span.End))
		color := waveRemove
		if clip.Keep {
			color = waveKeep
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
	if selected, ok := a.editSpan(); ok && selected.End > span.Start && selected.Start < span.End {
		left := timeX(body, span, max(selected.Start, span.Start))
		right := timeX(body, span, min(selected.End, span.End))
		p.Stroke(ui.Rect{X: left, Y: body.Y, W: right - left, H: body.H}, waveSelection, 0, 2)
	}
	for i := clipAt(clips, span.Start); i+1 < len(clips) && clips[i].End < span.End; i++ {
		x := timeX(body, span, clips[i].End)
		p.Line(x, body.Y, x, body.Y+body.H, 1, waveSelection.Alpha(.38))
		p.Fill(ui.Rect{X: x - 2, Y: body.Y + 1, W: 4, H: 7}, waveSelection, 1)
	}
	if a.waveDrag.boundary {
		x := timeX(body, span, a.waveDrag.end)
		p.Line(x, body.Y, x, body.Y+body.H, 2, waveSelection)
	}
	drawPlayhead(p, r, span, a.position, true)
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
	mid, half := r.Y+r.H/2, max(1, r.H/2-9)
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
	x := max(r.X+1, min(r.X+r.W-1, timeX(r, span, at)))
	p.Line(x, r.Y, x, r.Y+r.H, 2, accent)
	if marker {
		var head ui.Path
		head.MoveTo(x-5, r.Y).LineTo(x+5, r.Y).LineTo(x+5, r.Y+6).
			LineTo(x, r.Y+11).LineTo(x-5, r.Y+6).Close()
		p.FillPath(&head, accent)
	}
}

func (a *app) overview(c *ui.Context, item *asset, duration int64, allowed bool) {
	e := ui.Box(c.Key("overview")).Height(30).Radius(3).Background(waveBack).Clip().Focusable().
		Label("全局波形").Tooltip("点击定位 · 拖动窗口浏览")
	if duration == 0 {
		e.Children(func() { ui.Text(c, "").Height(30) })
		return
	}
	length := a.detailLength(duration)
	if allowed {
		pan := func(delta int64) { a.viewStart = max(0, min(duration-length, a.viewStart+delta)) }
		e.OnShortcut(0, ui.KeyLeft, func() { pan(-length / 4) })
		e.OnShortcut(0, ui.KeyRight, func() { pan(length / 4) })
	}
	x, _, _ := e.PointerPosition()
	w := e.Bounds().W
	if allowed && w > 0 {
		at := int64(float64(x/w) * float64(duration))
		switch {
		case a.overviewDrag.active && length < duration:
			e.Cursor(ui.CursorGrabbing)
		case length < duration && at >= a.viewStart && at <= a.viewStart+length:
			e.Cursor(ui.CursorGrab)
		default:
			e.Cursor(ui.CursorCrosshair)
		}
	}
	if _, _, pressed := e.Dragged(); pressed && allowed && w > 0 {
		at := int64(float64(max(0, min(x/w, 1))) * float64(duration))
		if !a.overviewDrag.active {
			a.stopPlayback()
			inside := at >= a.viewStart && at <= a.viewStart+length
			a.overviewDrag = overviewGesture{active: true, offset: at - a.viewStart, at: at, x: x, pendingClick: inside}
			if !inside || length == duration {
				a.overviewDrag.offset = length / 2
				a.overviewDrag.pendingClick = false
				a.selection = timeline.Span{}
				a.seek(at)
			}
		}
		if math.Abs(float64(x-a.overviewDrag.x)) >= 3 {
			a.overviewDrag.pendingClick = false
		}
		if !a.overviewDrag.pendingClick {
			a.viewStart = max(0, min(duration-length, at-a.overviewDrag.offset))
		}
	} else {
		if allowed && a.overviewDrag.pendingClick {
			a.selection = timeline.Span{}
			a.seek(a.overviewDrag.at)
		}
		a.overviewDrag = overviewGesture{}
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
