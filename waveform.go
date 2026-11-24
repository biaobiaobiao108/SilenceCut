package main

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"silencecut/internal/timeline"
)

const (
	detailDefault = 30 * timeline.Second
	detailMinimum = 2 * timeline.Second
	rulerHeight   = 22
	handleHeight  = 13
	hitRadius     = 6
	seekInterval  = 33 * time.Millisecond
)

var (
	waveKeptInk    = ui.Hex("#8BC8B3")
	waveRemovedInk = ui.Hex("#EB9B8F")
	waveOverview   = ui.Hex("#7F9791")
	overviewWindow = ui.Hex("#303C39")
)

type waveDragKind uint8

const (
	waveIdle waveDragKind = iota
	waveLocate
	waveBoundary
	wavePlayhead
	waveCanceled
)

type waveGesture struct {
	kind                waveDragKind
	leftKeep, selecting bool
	at, end, min, max   int64
	position            int64
	selection           timeline.Span
	x                   float32
	lastSeek            time.Time
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
	}
	a.wasPlaying, a.previewPlay = false, false
	if a.buildingCut {
		a.previewCancel()
		a.previewCancel, a.buildingCut = nil, false
	}
	if a.videoCut != nil {
		a.closePreview()
		a.openPreview(a.selected, false)
	}
}

func (a *app) timeline(c *ui.Context) {
	item := a.selected
	duration := item.info.Duration
	var kept int64
	for _, clip := range item.history.Clips {
		if clip.Keep {
			kept += clip.End - clip.Start
		}
	}
	allowed := !a.busy() && duration > 0
	playable := a.previewPlayable(kept)
	_, current := a.editSpan()
	length := a.detailLength(duration)
	minLength := min(duration, detailMinimum)
	zoom := 0.0
	if duration > minLength {
		zoom = math.Log(float64(duration)/float64(length)) / math.Log(float64(duration)/float64(minLength))
	}
	ui.Column(c.Key("timeline")).Grow(1).MinHeight(216).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
			mode := 0
			if a.originalPreview {
				mode = 1
			}
			tabStrip(c.Key("preview-mode"), "试听模式", &mode, []string{"过滤后", "原始"}, true, allowed,
				func() { a.setPreviewMode(mode == 1) })
			ui.Spacer(c)
			a.timelineReadout(c, duration, kept)
		})
		ui.Row(c).AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
			ui.Toolbar(c.Key("tools"), func() {
				icon, label := iconPlay, "播放"
				if a.buildingCut || a.previewPlay {
					icon, label = iconStop, "取消准备试听"
				} else if a.player != nil && a.player.Playing() {
					icon, label = iconPause, "暂停"
				}
				flatAction(c.Key("play"), label, playable, a.togglePlay).
					Width(controlHeight).Tooltip(label + "（空格）").Children(func() {
					glyph := ui.Icon(c, icon).Size(19, 19).TextColor(actionColor(playable, true))
					if !c.Preferences().ReduceMotion {
						glyph.ContentTransition(ui.ContentTransition{Animation: &ui.Animation{Duration: switchDuration}})
					}
				})
				iconButton(c.Key("stop"), iconStop, "停止", "停止并回到开头", allowed && a.player != nil, false, func() {
					a.stopPlayback()
					a.viewStart = 0
					a.seek(0)
				})
				ui.Divider(c).Height(16).AlignSelf(ui.Center).Margin(0, 3)
				editable := allowed && current
				iconButton(c.Key("keep"), iconKeep, "保留", "保留选区或当前片段", editable, false, func() { a.edit(true) })
				iconButton(c.Key("discard"), iconDiscard, "移除", "移除选区或当前片段", editable, false, func() { a.edit(false) })
				iconButton(c.Key("split"), iconSplit, "反转 0.2 秒", "从游标起反转最多 0.2 秒，止于当前片段边界",
					editable && a.position < duration, false, a.invertAtPlayhead)
				ui.Divider(c).Height(16).AlignSelf(ui.Center).Margin(0, 3)
				iconButton(c.Key("undo"), iconUndo, "撤销", "撤销", allowed && item.history.CanUndo(), false, func() { a.history(false) })
				iconButton(c.Key("redo"), iconRedo, "重做", "重做", allowed && item.history.CanRedo(), false, func() { a.history(true) })
				iconButton(c.Key("sample-threshold"), iconSample, "采集游标处音量", "采集游标处音量作为自定义阈值",
					allowed && len(item.peaks) > 0, false, a.sampleThreshold)
			}).Label("时间轴工具").Grow(1).Gap(2).Padding(0)
			ui.Row(c.Key("zoom-controls")).Gap(2).AlignItems(ui.Center).Shrink(0).Children(func() {
				iconButton(c.Key("zoom-out"), iconZoomOut, "缩小波形", "缩小波形", length < duration, false,
					func() { a.setZoom(length * 3 / 2) })
				ui.Slider(c.Key("zoom"), &zoom, 0, 1).Width(64).Label("波形缩放").Disabled(duration <= minLength).
					OnChange(func() {
						a.setZoom(int64(math.Round(float64(duration) * math.Pow(float64(minLength)/float64(duration), zoom))))
					})
				iconButton(c.Key("zoom-in"), iconZoomIn, "放大波形", "放大波形", length > minLength, false,
					func() { a.setZoom(length * 2 / 3) })
			})
		})
		if item.info.Video {
			ui.Box(c.Key("video-preview")).Grow(1.15).MinHeight(120).Center().
				Radius(controlRadius).Background(screen).Clip().Children(func() {
				ready := a.video != nil && a.video.Ready()
				if a.video != nil {
					// Building Video pumps the native decoder, including while it opens.
					video := ui.Video(c.Key(a.previewSeq), a.video).Fill().Fit(ui.Contain)
					target := float32(0)
					if ready {
						target = 1
					}
					video.Opacity(video.Animate("ready", target, revealDuration))
					if !ready && a.video.Ready() {
						c.Invalidate()
					}
				}
				if !ready || a.buildingCut {
					ui.Column(c.Key("video-feedback")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Children(func() {
						a.previewFeedback(c, item)
					})
				}
			})
		}
		if duration == 0 {
			ui.Column(c.Key("wave")).Grow(1).MinHeight(112).Padding(railInset).Background(waveBack).Center().Children(func() {
				switch {
				case item.analyzing:
					ui.Text(c, "正在分析素材…").FontSize(13).TextColor(muted)
				case item.issue != "":
					ui.Text(c, item.issue).Wrap().FontSize(12).TextColor(waveRemovedInk)
				default:
					ui.Text(c, "素材待分析").FontSize(13).TextColor(muted)
				}
			})
			ui.Box(c.Key("overview")).Height(30).Shrink(0).Background(waveBack)
		} else {
			a.wave(c, item, duration, allowed)
			a.overview(c, item, duration, allowed)
		}
	})
}

func (a *app) timelineReadout(c *ui.Context, duration, kept int64) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, clock(a.position)).FontFeatures("tnum").FontSize(16).Bold()
		if duration == 0 {
			return
		}
		ui.Text(c, "/ "+clock(duration)).FontFeatures("tnum").FontSize(12).TextColor(muted)
		label, color := "保留 "+clock(kept), waveKeptInk
		if kept == 0 {
			label, color = "无保留片段", waveRemovedInk
		}
		ui.Text(c, label).FontFeatures("tnum").FontSize(12).TextColor(color)
	})
}

func (a *app) invertAtPlayhead() {
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

func (a *app) wave(c *ui.Context, item *asset, duration int64, allowed bool) {
	e := ui.Box(c.Key("wave")).Grow(1).MinHeight(112).Background(waveBack).Clip().
		Focusable().FocusRing(true).Label("音频波形和播放位置")
	if allowed {
		nudge := func(delta int64) {
			a.selection = timeline.Span{}
			a.stopPlayback()
			a.seek(a.position + delta)
			a.followPlayhead()
		}
		if a.waveDrag.kind == waveIdle {
			e.OnShortcut(0, ui.KeyLeft, func() { nudge(-100_000) })
			e.OnShortcut(0, ui.KeyRight, func() { nudge(100_000) })
		}
	}
	length := a.detailLength(duration)
	x, y, over := e.PointerPosition()
	w := e.Bounds().W
	_, _, pressed := e.Dragged()
	if w > 0 {
		a.handleWave(c, e, item, w, x, y, length, over, pressed, allowed)
	}
	e.Children(func() {
		ui.Box(c.Key(item.path)).Fill().Transition(reveal(c, &ui.Motion{}, false)).
			Draw(func(p *ui.Painter, r ui.Rect) { a.drawWave(p, r, item, length) })
		if !item.info.Video && (a.previewCancel != nil || a.previewIssue != "") {
			ui.Box(c.Key("audio-preview-feedback")).Absolute().Right(8).Bottom(8).Radius(controlRadius).
				Padding(6, 8).MaxWidth(max(0, w-16)).Background(rail).Children(func() { a.previewFeedback(c, item) })
		}
	})
}

func (a *app) handleWave(c *ui.Context, e ui.Element, item *asset, w, x, y float32, length int64, over, pressed, allowed bool) {
	drag := &a.waveDrag
	if drag.kind != waveIdle && drag.kind != waveCanceled && (!allowed || e.PressCanceled()) {
		a.cancelWave()
	}
	if drag.kind == waveCanceled {
		if !pressed {
			*drag = waveGesture{}
		}
		return
	}
	if !allowed {
		return
	}
	span := timeline.Span{Start: a.viewStart, End: a.viewStart + length}
	at := span.Start + int64(math.Round(float64(max(0, min(x/w, 1)))*float64(length)))
	kind, boundary := waveLocate, -1
	if (over || pressed) && drag.kind == waveIdle {
		kind, boundary = waveHit(item.history.Clips, span, a.position, w, x, y, at)
	}
	if drag.kind == waveIdle && pressed {
		a.stopPlayback()
		*drag = waveGesture{kind: kind, x: x, at: at, end: at, position: a.position, selection: a.selection}
		switch kind {
		case waveBoundary:
			left, right := item.history.Clips[boundary], item.history.Clips[boundary+1]
			drag.at, drag.end, drag.min, drag.max, drag.leftKeep = left.End, left.End, left.Start, right.End, left.Keep
		case waveLocate:
			a.selection = timeline.Span{}
			a.seek(at)
		case wavePlayhead:
			drag.end = a.position
		}
	}
	if drag.kind != waveIdle {
		kind = drag.kind
	}
	switch kind {
	case wavePlayhead:
		cursor := ui.CursorGrab
		if pressed {
			cursor = ui.CursorGrabbing
		}
		e.Cursor(cursor)
	case waveBoundary:
		e.Cursor(ui.CursorResizeEW)
	default:
		e.Cursor(ui.CursorCrosshair)
	}
	if pressed {
		switch kind {
		case wavePlayhead:
			target := drag.position + int64(math.Round((float64(x)-float64(drag.x))/float64(w)*float64(length)))
			switch {
			case x < 0:
				target = span.Start
			case x > w:
				target = span.End
			}
			a.position = max(span.Start, min(span.End, target))
			now := c.Now()
			if a.position != drag.end && (drag.lastSeek.IsZero() || now.Sub(drag.lastSeek) >= seekInterval) {
				a.seek(a.position)
				drag.lastSeek, drag.end = now, a.position
			}
		case waveBoundary:
			if x != drag.x || drag.end != drag.at {
				delta := int64(math.Round((float64(x) - float64(drag.x)) / float64(w) * float64(length)))
				drag.end = max(drag.min, min(drag.max, drag.at+delta))
			}
		case waveLocate:
			if drag.selecting || math.Abs(float64(x-drag.x)) >= 3 {
				drag.selecting = true
				a.selection = timeline.Span{Start: min(drag.at, at), End: max(drag.at, at)}
			}
		}
		return
	}
	switch drag.kind {
	case wavePlayhead:
		a.seek(a.position)
	case waveBoundary:
		if drag.end != drag.at {
			item.history.Edit(timeline.Span{Start: min(drag.at, drag.end), End: max(drag.at, drag.end)},
				(drag.end > drag.at) == drag.leftKeep)
			a.seek(drag.end)
		}
	}
	*drag = waveGesture{}
}

func (a *app) cancelWave() {
	drag := &a.waveDrag
	a.selection = drag.selection
	a.seek(drag.position)
	drag.kind = waveCanceled
}

func waveHit(clips []timeline.Clip, span timeline.Span, position int64, w, x, y float32, at int64) (waveDragKind, int) {
	if y >= rulerHeight && y < rulerHeight+handleHeight {
		tolerance := int64(math.Ceil(float64(span.End-span.Start) * hitRadius / float64(w)))
		if boundary := nearestBoundary(clips, at, tolerance); boundary >= 0 &&
			clips[boundary].End > span.Start && clips[boundary].End < span.End &&
			math.Abs(float64(x-timeX(ui.Rect{W: w}, span, clips[boundary].End))) <= hitRadius {
			return waveBoundary, boundary
		}
	}
	if position >= span.Start && position <= span.End &&
		math.Abs(float64(x-playheadX(ui.Rect{W: w}, span, position))) <= hitRadius {
		return wavePlayhead, -1
	}
	return waveLocate, -1
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
		p.Stroke(ui.Rect{X: left, Y: body.Y, W: right - left, H: body.H}, waveSelection, 0, 1.5)
	}
	for i := clipAt(clips, span.Start); i+1 < len(clips) && clips[i].End < span.End; i++ {
		x := timeX(body, span, clips[i].End)
		p.Line(x, body.Y, x, body.Y+body.H, 1, waveSelection.Alpha(.25))
		p.Fill(ui.Rect{X: x - 1, Y: body.Y + 2, W: 2, H: 6}, waveSelection.Alpha(.72), 1)
	}
	if a.waveDrag.kind == waveBoundary {
		x := playheadX(body, span, a.waveDrag.end)
		p.Line(x, body.Y, x, body.Y+body.H, 2, waveSelection)
		labelX := max(r.X+4, min(x+6, r.X+r.W-92))
		p.Fill(ui.Rect{X: labelX - 3, Y: r.Y + 1, W: 88, H: rulerHeight - 2}, waveBack, 0)
		p.Text(labelX, r.Y+1, clock(a.waveDrag.end), 11, waveSelection)
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
	e := ui.Box(c.Key("overview")).Height(30).Shrink(0).Background(waveBack).Clip().Focusable().FocusRing(true).
		Label("全局波形").Tooltip("点击定位 · 拖动窗口浏览")
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
