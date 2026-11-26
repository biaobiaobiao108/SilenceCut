package main

import (
	"math"
	"sort"
	"time"

	"github.com/egoist/mygo/ui"

	"silencecut/internal/timeline"
)

const (
	detailDefault     = 30 * timeline.Second
	detailMinimum     = 2 * timeline.Second
	rulerHeight       = 22
	handleHeight      = 13
	hitRadius         = 6
	waveDragSlop      = 3
	selectionGrip     = 4
	boundaryNudge     = 100_000
	seekInterval      = 33 * time.Millisecond
	videoSeekInterval = 100 * time.Millisecond
	waveEdgeZone      = 24
	wavePanTick       = 16 * time.Millisecond
	wavePanRate       = .3
)

var (
	waveKeptInk    = ui.Hex("#8BC8B3")
	waveRemovedInk = ui.Hex("#EB9B8F")
	waveOverview   = ui.Hex("#829A94")
	overviewWindow = ui.Hex("#303C39")
)

type waveDragKind uint8

const (
	waveIdle waveDragKind = iota
	waveLocate
	waveBoundary
	wavePlayhead
	waveScrub
	waveSelectStart
	waveSelectEnd
	waveSelectMove
)

type waveGesture struct {
	kind              waveDragKind
	leftKeep, moved   bool
	at, end, min, max int64
	offset, viewStart int64
	position          int64
	selection, draft  timeline.Span
	x                 float32
	lastSeek, lastPan time.Time
}

type overviewGesture struct {
	active, moved     bool
	offset, viewStart int64
	x                 float32
}

type timelineState struct {
	position        int64
	selection       timeline.Span
	viewStart       int64
	viewLength      int64
	waveDrag        waveGesture
	overviewDrag    overviewGesture
	waveColumns     []timeline.Peak
	overviewColumns []timeline.Peak
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
	a.scheduleRecovery()
}

func (a *app) panWave(dx float32, duration, length int64, width float32) bool {
	if dx == 0 || width <= 0 || length >= duration {
		return false
	}
	delta := int64(math.Round(float64(length) * float64(dx) / float64(width)))
	if delta == 0 {
		delta = 1
		if dx < 0 {
			delta = -1
		}
	}
	next := max(0, min(duration-length, a.viewStart+delta))
	if next == a.viewStart {
		return true
	}
	a.viewStart = next
	a.scheduleRecovery()
	return true
}

func (a *app) panWaveDrag(c *ui.Context, drag *waveGesture, x, width float32, duration, length int64) bool {
	now := c.Now()
	elapsed := now.Sub(drag.lastPan)
	if elapsed < 0 {
		elapsed = 0
	}
	elapsed = min(elapsed, 50*time.Millisecond)
	drag.lastPan = now
	start := a.viewStart

	direction, depth := 0, float32(0)
	switch {
	case x < waveEdgeZone:
		direction, depth = -1, waveEdgeZone-x
	case x > width-waveEdgeZone:
		direction, depth = 1, x-(width-waveEdgeZone)
	}
	if direction != 0 && length < duration {
		if elapsed > 0 {
			factor := 1 + min(1, max(0, depth)/waveEdgeZone)
			delta := max(1, int64(math.Round(float64(length)*wavePanRate*float64(factor)*elapsed.Seconds())))
			a.viewStart = max(0, min(duration-length, a.viewStart+int64(direction)*delta))
		}
		if direction < 0 && a.viewStart > 0 || direction > 0 && a.viewStart < duration-length {
			c.After(wavePanTick)
		}
	}
	return a.viewStart != start
}

func clipAt(clips []timeline.Clip, at int64) int {
	return sort.Search(len(clips), func(i int) bool { return clips[i].End > at })
}

func (a *app) stopPlayback() {
	if a.player != nil {
		a.player.Pause()
	}
	a.wasPlaying = false
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
	ui.Column(c.Key("timeline")).Grow(1).MinHeight(300).Gap(10).Children(func() {
		ui.Row(c).Height(32).AlignItems(ui.Center).Gap(10).Shrink(0).Children(func() {
			mode := 0
			if a.originalPreview {
				mode = 1
			}
			tabs := tabStrip(c.Key("preview-mode"), "试听模式", &mode, []string{"过滤后", "原始"}, true, allowed,
				func() { a.setPreviewMode(mode == 1) })
			if item.info.Video {
				tabs.Description("过滤后试听使用系统播放器近似跳过切点，精确结果以导出文件为准")
			}
			ui.Spacer(c)
			a.timelineReadout(c, duration, kept)
		})
		tools := ui.Row(c.Key("timeline-tools")).Height(40).AlignItems(ui.Center).Gap(2).Shrink(0).
			Padding(0, 5).Radius(controlRadius).Background(surface).BorderColor(border).BorderWidth(1).Label("时间轴工具")
		tools.Children(func() {
			icon, label := iconPlay, "播放"
			if a.player != nil && a.player.Playing() {
				icon, label = iconPause, "暂停"
			}
			flatAction(c.Key("play"), label, playable, a.togglePlay).
				Width(38).Children(func() {
				glyph := ui.Icon(c, icon).Size(19, 19).TextColor(actionColor(playable, true))
				if !c.Preferences().ReduceMotion {
					glyph.ContentTransition(ui.ContentTransition{Animation: &ui.Animation{Duration: switchDuration}})
				}
			})
			iconButton(c.Key("stop"), iconStop, "停止", allowed && a.player != nil, false, func() {
				a.stopPlayback()
				a.viewStart = 0
				a.seek(0)
			}).Description("停止并回到开头")
			ui.Divider(c).Height(16).AlignSelf(ui.Center).Margin(0, 3)
			editable := allowed && current
			iconButton(c.Key("keep"), iconKeep, "保留", editable, false, func() { a.edit(true) })
			iconButton(c.Key("discard"), iconDiscard, "移除", editable, false, func() { a.edit(false) })
			iconButton(c.Key("invert"), iconInvert, "反转短段", editable && a.position < duration, false, a.invertAtPlayhead).
				Description("从游标起反转最多 0.2 秒，止于当前片段边界")
			ui.Divider(c).Height(16).AlignSelf(ui.Center).Margin(0, 3)
			iconButton(c.Key("undo"), iconUndo, "撤销", allowed && item.history.CanUndo(), false, func() { a.history(false) })
			iconButton(c.Key("redo"), iconRedo, "重做", allowed && item.history.CanRedo(), false, func() { a.history(true) })
			iconButton(c.Key("sample-threshold"), iconSample, "采集游标处音量", allowed && len(item.peaks) > 0, false, a.sampleThreshold).
				Description("以采集的音量作为自定义阈值")
			ui.Spacer(c)
			ui.Row(c.Key("zoom-controls")).Gap(3).AlignItems(ui.Center).Shrink(0).Children(func() {
				iconButton(c.Key("zoom-out"), iconZoomOut, "缩小波形", length < duration, false,
					func() { a.setZoom(length * 3 / 2) })
				ui.Slider(c.Key("zoom"), &zoom, 0, 1).Width(64).Label("波形缩放").Disabled(duration <= minLength).
					OnChange(func() {
						a.setZoom(int64(math.Round(float64(duration) * math.Pow(float64(minLength)/float64(duration), zoom))))
					})
				iconButton(c.Key("zoom-in"), iconZoomIn, "放大波形", length > minLength, false,
					func() { a.setZoom(length * 2 / 3) })
			})
		})
		pending := item.pendingWave
		if !item.offline && (item.info.Video || pending != nil && pending.info.Video) {
			ui.Box(c.Key("video-preview")).Grow(1.15).MinHeight(120).Center().
				Radius(surfaceRadius).Background(screen).BorderColor(border).BorderWidth(1).Clip().Children(func() {
				ready := a.video != nil && a.video.Ready()
				if a.video != nil {
					// Building Video pumps the native decoder, including while it opens.
					video := ui.Video(c.Key(a.previewSeq), a.video).Fill().Fit(ui.Contain)
					target := float32(0)
					if ready {
						target = 1
					}
					if c.Preferences().ReduceMotion {
						video.Opacity(target)
					} else {
						video.Opacity(video.Animate("ready", target, revealDuration))
					}
					if !ready && a.video.Ready() {
						c.Invalidate()
					}
				}
				if !ready && pending == nil {
					ui.Column(c.Key("video-feedback")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
						Background(screen.Alpha(.72)).Center().Children(func() {
						a.previewFeedback(c, item)
					})
				}
			})
		}
		if duration == 0 {
			placeholder := ui.Column(c.Key("wave")).Grow(1).MinHeight(112).Radius(surfaceRadius).
				Background(waveBack).BorderColor(border).BorderWidth(1).Clip().Center()
			if pending != nil {
				placeholder.Label("正在分析的音频波形").Children(func() {
					ui.Box(c.Key(item.path)).Fill().Transition(reveal(c, &ui.Motion{}, false)).
						Draw(func(p *ui.Painter, r ui.Rect) {
							span := timeline.Span{End: detailDefault}
							body := ui.Rect{X: r.X, Y: r.Y + rulerHeight, W: r.W, H: r.H - rulerHeight}
							mid := body.Y + body.H/2
							drawTimeRuler(p, r, span)
							p.Line(body.X, mid, body.X+body.W, mid, 1, border)
							a.waveColumns = pending.wave.Columns(span, pending.info.Rate, max(1, int(body.W)), a.waveColumns)
							drawEnvelope(p, body, a.waveColumns, pending.wave.Amplitude, func(float32) ui.Color { return waveOverview })
						})
				})
			} else {
				placeholder.Padding(railInset).Children(func() {
					switch {
					case item.analyzing:
						ui.Text(c, "正在分析素材…").FontSize(13).TextColor(muted)
					case item.issue != "":
						ui.Text(c, item.issue).Wrap().FontSize(12).TextColor(waveRemovedInk)
					default:
						ui.Text(c, "素材待分析").FontSize(13).TextColor(muted)
					}
				})
			}
			ui.Box(c.Key("overview")).Height(28).Shrink(0).Radius(controlRadius).Background(waveBack).BorderColor(border).BorderWidth(1)
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
		a.markProject()
		a.selection = timeline.Span{}
	}
}

func (a *app) wave(c *ui.Context, item *asset, duration int64, allowed bool) {
	var waveHandle ui.Handle
	e := ui.Box(c.Key("wave")).Grow(1).MinHeight(112).Radius(surfaceRadius).Background(waveBack).
		BorderColor(border).BorderWidth(1).Clip().
		Focusable().FocusRing(true).Label("音频波形和播放位置").
		Description("点击定位，拖动建立选区，拖动选区内部移动，拖动两端调整范围。Escape 取消拖动或清除选区。横向滚动平移视图，左右方向键移动游标，Alt 加左右方向键调整当前片段起点，Alt+Shift 调整终点").
		ContextMenu(a.waveMenu(item)).Bind(&waveHandle)
	clips := item.history.Clips
	if selected, ok := a.timelineSelection(item); ok {
		e.Value("选区 " + clock(selected.Start) + " 至 " + clock(selected.End))
	} else if i := clipAt(clips, a.position); i < len(clips) {
		kind := "移除片段"
		if clips[i].Keep {
			kind = "保留片段"
		}
		e.Value(kind)
	}
	if allowed {
		nudge := func(delta int64) {
			a.selection = timeline.Span{}
			a.stopPlayback()
			a.seek(a.position + delta)
			a.followPlayhead()
		}
		if a.waveDrag.kind == waveIdle {
			if a.selection.End > a.selection.Start {
				e.OnShortcut(0, ui.KeyEscape, func() { a.selection = timeline.Span{} })
			}
			e.OnShortcut(0, ui.KeyLeft, func() { nudge(-100_000) })
			e.OnShortcut(0, ui.KeyRight, func() { nudge(100_000) })
			e.OnShortcut(ui.Alt, ui.KeyLeft, func() { a.nudgeCurrentBoundary(true, -boundaryNudge) })
			e.OnShortcut(ui.Alt, ui.KeyRight, func() { a.nudgeCurrentBoundary(true, boundaryNudge) })
			e.OnShortcut(ui.Alt|ui.Shift, ui.KeyLeft, func() { a.nudgeCurrentBoundary(false, -boundaryNudge) })
			e.OnShortcut(ui.Alt|ui.Shift, ui.KeyRight, func() { a.nudgeCurrentBoundary(false, boundaryNudge) })
		}
	}
	length := a.detailLength(duration)
	x, y, over := e.PointerPosition()
	w := e.Bounds().W
	e.HandleInput(func(ev ui.InputEvent) bool {
		if a.selected != item {
			return false
		}
		width := waveHandle.Bounds(c).W
		length := a.detailLength(duration)
		switch ev.Kind {
		case ui.InputPointerDown:
			if a.busy() || width <= 0 || a.waveDrag.kind != waveIdle || a.overviewDrag.active {
				return false
			}
			switch ev.Button {
			case 0:
				a.beginWave(item, ev.X, ev.Y, width, length, ev.Time)
				return true
			case 1:
				a.seek(a.viewStart + waveTime(ev.X, width, length))
			}
		case ui.InputPointerMove:
			if ev.Button == 0 && !a.busy() && width > 0 && a.waveDrag.kind != waveIdle && a.waveDrag.markMovement(ev.X, ev.Time) {
				a.updateWaveDrag(ev.Time, item, ev.X, width, length, true)
				return true
			}
		case ui.InputPointerUp:
			if ev.Button != 0 || a.waveDrag.kind == waveIdle {
				return false
			}
			if ev.Canceled || a.busy() || width <= 0 {
				a.cancelWave()
			} else {
				a.finishWave(item, ev.X, ev.Y, width, length, ev.Time)
			}
			return true
		case ui.InputScroll:
			if !a.busy() && a.waveDrag.kind == waveIdle && !a.overviewDrag.active {
				dx := ev.DX
				if dx == 0 && ev.Mods&ui.Shift != 0 {
					dx = ev.DY
				}
				return a.panWave(dx, duration, length, width)
			}
		}
		return false
	})
	if w > 0 {
		a.trackWave(c, e, item, w, x, y, length, over, allowed)
	}
	e.Children(func() {
		ui.Box(c.Key(item.path)).Fill().Transition(reveal(c, &ui.Motion{}, false)).
			Draw(func(p *ui.Painter, r ui.Rect) { a.drawWave(p, r, item, length) })
		if !item.info.Video && a.previewIssue != "" {
			ui.Box(c.Key("audio-preview-feedback")).Absolute().Right(8).Bottom(8).Radius(controlRadius).
				Padding(6, 8).MaxWidth(max(0, w-16)).Background(rail).Children(func() { a.previewFeedback(c, item) })
		}
	})
}

func (a *app) waveMenu(item *asset) func(*ui.Menu) {
	return func(m *ui.Menu) {
		enabled := !a.busy() && a.selected == item && item.info.Duration > 0
		kept := int64(0)
		for _, clip := range item.history.Clips {
			if clip.Keep {
				kept += clip.End - clip.Start
			}
		}
		playable := enabled && a.previewPlayable(kept)
		if m.Item("从此处播放/暂停").Disabled(!playable).Chosen() {
			a.togglePlay()
		}
		if _, ok := a.timelineSelection(item); ok {
			if m.Item("导出选中区域…").Disabled(!enabled || item.offline || item.issue != "").Chosen() {
				a.chooseSelectionExport()
			}
		}
		m.Separator()
		editable := false
		if enabled {
			_, editable = a.editSpan()
		}
		if m.Item("保留").Disabled(!editable).Chosen() {
			a.edit(true)
		}
		if m.Item("移除").Disabled(!editable).Chosen() {
			a.edit(false)
		}
		m.Separator()
		if m.Item("撤销").Disabled(!enabled || !item.history.CanUndo()).Chosen() {
			a.history(false)
		}
		if m.Item("重做").Disabled(!enabled || !item.history.CanRedo()).Chosen() {
			a.history(true)
		}
	}
}

func (a *app) beginWave(item *asset, x, y, width float32, length int64, now time.Time) {
	span := timeline.Span{Start: a.viewStart, End: a.viewStart + length}
	at := span.Start + waveTime(x, width, length)
	clips := item.history.Clips
	kind, boundary := waveHit(clips, span, a.selection, a.position, width, x, y, at)
	a.stopPlayback()
	drag := waveGesture{
		kind: kind, x: x, at: at, end: at, position: a.position, selection: a.selection,
		viewStart: a.viewStart, lastPan: now,
	}
	switch kind {
	case waveBoundary:
		left, right := clips[boundary], clips[boundary+1]
		drag.at, drag.end, drag.min, drag.max, drag.leftKeep = left.End, left.End, left.Start, right.End, left.Keep
	case wavePlayhead:
		drag.end, drag.offset = a.position, a.position-at
	case waveScrub:
		drag.end = a.position
	case waveSelectStart:
		drag.offset = a.selection.Start - at
	case waveSelectEnd:
		drag.offset = a.selection.End - at
	}
	a.waveDrag = drag
}

func (a *app) trackWave(c *ui.Context, e ui.Element, item *asset, w, x, y float32, length int64, over, allowed bool) {
	drag := &a.waveDrag
	if !allowed {
		if drag.kind != waveIdle {
			a.cancelWave()
		}
		return
	}
	if a.overviewDrag.active || !over && drag.kind == waveIdle {
		return
	}
	kind := drag.kind
	if kind == waveIdle {
		span := timeline.Span{Start: a.viewStart, End: a.viewStart + length}
		kind, _ = waveHit(item.history.Clips, span, a.selection, a.position, w, x, y, span.Start+waveTime(x, w, length))
	} else if drag.moved && kind != waveBoundary && a.panWaveDrag(c, drag, x, w, item.info.Duration, length) {
		a.updateWaveDrag(c.Now(), item, x, w, length, true)
	}
	switch kind {
	case wavePlayhead, waveSelectMove, waveScrub:
		cursor := ui.CursorGrab
		if kind == waveScrub {
			cursor = ui.CursorCrosshair
		}
		if drag.moved {
			cursor = ui.CursorGrabbing
		}
		e.Cursor(cursor)
	case waveBoundary, waveSelectStart, waveSelectEnd:
		e.Cursor(ui.CursorResizeEW)
	default:
		e.Cursor(ui.CursorCrosshair)
	}
}

func (drag *waveGesture) markMovement(x float32, now time.Time) bool {
	if !drag.moved && math.Abs(float64(x-drag.x)) >= waveDragSlop {
		drag.moved = true
		drag.lastPan = now
	}
	return drag.moved
}

func (a *app) finishWave(item *asset, x, y, w float32, length int64, now time.Time) {
	drag := &a.waveDrag
	if drag.markMovement(x, now) {
		a.updateWaveDrag(now, item, x, w, length, false)
	} else {
		at := a.viewStart + waveTime(x, w, length)
		if y >= rulerHeight && drag.kind != waveSelectStart && drag.kind != waveSelectEnd &&
			(at < a.selection.Start || at >= a.selection.End) {
			a.selection = timeline.Span{}
		}
		if at != a.position {
			a.seek(at)
		}
	}
	a.scheduleRecovery()
	*drag = waveGesture{}
}

func (a *app) updateWaveDrag(now time.Time, item *asset, x, w float32, length int64, pressed bool) {
	drag := &a.waveDrag
	duration := item.info.Duration
	at := a.viewStart + waveTime(x, w, length)
	target := at + drag.offset
	if x < 0 || x > w {
		target = at
	}
	switch drag.kind {
	case wavePlayhead, waveScrub:
		a.position = max(0, min(duration, target))
		interval := seekInterval
		if item.info.Video {
			// Give the native player time to present a frame during a scrub.
			interval = videoSeekInterval
		}
		if a.position != drag.end && (!pressed || drag.lastSeek.IsZero() || now.Sub(drag.lastSeek) >= interval) {
			a.seek(a.position)
			drag.lastSeek, drag.end = now, a.position
		}
	case waveBoundary:
		delta := int64(math.Round(float64(x-drag.x) / float64(w) * float64(length)))
		drag.end = max(drag.min, min(drag.max, drag.at+delta))
		if !pressed && drag.end != drag.at {
			item.history.Edit(timeline.Span{Start: min(drag.at, drag.end), End: max(drag.at, drag.end)},
				(drag.end > drag.at) == drag.leftKeep)
			a.markProject()
			a.seek(drag.end)
		}
	case waveLocate:
		drag.draft = timeline.Span{Start: min(drag.at, at), End: max(drag.at, at)}
	case waveSelectStart:
		drag.draft = timeline.Span{Start: max(0, min(target, drag.selection.End-1)), End: drag.selection.End}
	case waveSelectEnd:
		drag.draft = timeline.Span{Start: drag.selection.Start, End: min(duration, max(target, drag.selection.Start+1))}
	case waveSelectMove:
		size := drag.selection.End - drag.selection.Start
		// Keep the press offset even when the pointer goes beyond the viewport.
		pointer := a.viewStart + int64(math.Round(float64(x)/float64(w)*float64(length)))
		start := max(0, min(duration-size, drag.selection.Start+pointer-drag.at))
		drag.draft = timeline.Span{Start: start, End: start + size}
	}
	if !pressed && drag.draft.End > drag.draft.Start {
		a.selection = drag.draft
	}
}

func (a *app) cancelWave() {
	drag := &a.waveDrag
	a.viewStart = drag.viewStart
	if a.position != drag.position || (drag.kind == wavePlayhead || drag.kind == waveScrub) && drag.end != drag.position {
		a.seek(drag.position)
	}
	a.scheduleRecovery()
	*drag = waveGesture{}
}

func waveHit(clips []timeline.Clip, span, selection timeline.Span, position int64, w, x, y float32, at int64) (waveDragKind, int) {
	r := ui.Rect{W: w}
	playhead := position >= span.Start && position <= span.End && math.Abs(float64(x-playheadX(r, span, position))) <= hitRadius
	if y < rulerHeight {
		if playhead {
			return wavePlayhead, -1
		}
		return waveScrub, -1
	}
	if y < rulerHeight+handleHeight {
		tolerance := int64(math.Ceil(float64(span.End-span.Start) * hitRadius / float64(w)))
		if boundary := nearestBoundary(clips, at, tolerance); boundary >= 0 &&
			clips[boundary].End > span.Start && clips[boundary].End < span.End &&
			math.Abs(float64(x-timeX(r, span, clips[boundary].End))) <= hitRadius {
			return waveBoundary, boundary
		}
	}
	if selection.End > selection.Start && selection.End > span.Start && selection.Start < span.End {
		best := float64(hitRadius)
		kind := waveIdle
		for i, edge := range [...]int64{selection.Start, selection.End} {
			if edge >= span.Start && edge <= span.End {
				distance := math.Abs(float64(x - selectionGripX(r, span, selection, i == 0)))
				if distance <= best {
					best = distance
					kind = waveSelectStart + waveDragKind(i)
				}
			}
		}
		if kind != waveIdle {
			return kind, -1
		}
		if at >= selection.Start && at < selection.End {
			return waveSelectMove, -1
		}
	}
	if playhead {
		return wavePlayhead, -1
	}
	return waveLocate, -1
}

func waveTime(x, width float32, length int64) int64 {
	return int64(math.Round(float64(max(0, min(x, width))) / float64(width) * float64(length)))
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

func selectionGripX(r ui.Rect, span, selection timeline.Span, start bool) float32 {
	left, right := timeX(r, span, selection.Start), timeX(r, span, selection.End)
	inset := float32(selectionGrip) / 2
	if selection.Start >= span.Start && selection.End <= span.End && right-left < selectionGrip {
		// Keep both ends reachable when the selected time fits inside one pixel.
		left = max(r.X+inset, min(r.X+r.W-inset-selectionGrip, (left+right-selectionGrip)/2))
		right = left + selectionGrip
	}
	x := right
	if start {
		x = left
	}
	return max(r.X+inset, min(r.X+r.W-inset, x))
}

func (a *app) nudgeCurrentBoundary(start bool, delta int64) {
	if a.selected == nil || a.selected.info.Duration <= 0 || a.busy() {
		return
	}
	clips := a.selected.history.Clips
	index := clipAt(clips, min(a.position, a.selected.info.Duration-1))
	boundary := index
	if !start {
		boundary++
	}
	if boundary <= 0 || boundary >= len(clips) {
		return
	}
	left, right := clips[boundary-1], clips[boundary]
	at := left.End
	target := max(left.Start, min(right.End, at+delta))
	if target == at {
		return
	}
	a.stopPlayback()
	a.selected.history.Edit(
		timeline.Span{Start: min(at, target), End: max(at, target)},
		(target > at) == left.Keep,
	)
	a.markProject()
	a.seek(target)
}
