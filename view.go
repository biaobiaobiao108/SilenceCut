package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"silencecut/internal/timeline"
)

const (
	workspaceInset = 18
	railInset      = 14
	sideWidth      = 172
	settingsWidth  = 228
	sectionGap     = 12
	controlHeight  = 34
	controlRadius  = 5
	headerHeight   = 54
	wideWorkspace  = 1080
	hoverDuration  = 110 * time.Millisecond
	switchDuration = 140 * time.Millisecond
	revealDuration = 180 * time.Millisecond
)

var (
	worktop       = ui.Hex("#17191B")
	rail          = ui.Hex("#1D2022")
	border        = ui.Hex("#34383B")
	ink           = ui.Hex("#EFEEE9")
	muted         = ui.Hex("#ABB0B4")
	disabledInk   = ui.Hex("#73787C")
	accent        = ui.Hex("#F3BD66")
	accentHover   = ui.Hex("#F9CB80")
	accentPressed = ui.Hex("#DEA653")
	hoverFace     = ui.Hex("#2A2E30")
	pressedFace   = ui.Hex("#363B3D")
	screen        = ui.Hex("#111315")
	waveBack      = ui.Hex("#202426")
	waveKeep      = ui.Hex("#24332F")
	waveRemove    = ui.Hex("#352729")
	waveSelection = ui.Hex("#D4DDD8")
)

var appTheme = func() *ui.Theme {
	theme := ui.DarkTheme()
	theme.Background = worktop
	theme.Surface, theme.SurfaceHover, theme.SurfacePressed = rail, hoverFace, pressedFace
	theme.Border, theme.Text, theme.TextMuted = border, ink, muted
	theme.Accent, theme.AccentHover, theme.AccentPressed = accent, accentHover, accentPressed
	theme.AccentText = worktop
	theme.InactiveAccent, theme.Focus, theme.Danger = accent, accent, waveRemovedInk
	theme.Radius = controlRadius
	return theme
}()

func (a *app) view(c *ui.Context) {
	c.SetTheme(appTheme)
	width, _ := c.Size()
	narrow := width < wideWorkspace
	if !narrow {
		a.settingsOpen = false
	}
	if a.waveDrag.kind > waveIdle && a.waveDrag.kind < waveCanceled && c.Shortcut(0, ui.KeyEscape) {
		a.cancelWave()
	}
	a.previewTick(c)
	c.OnWindowShortcut(0, ui.KeySpace, a.togglePlay)
	mod := ui.Cmd
	c.OnWindowShortcut(mod, ui.KeyO, a.chooseFiles)
	c.OnWindowShortcut(mod, ui.KeyZ, func() { a.history(false) })
	c.OnWindowShortcut(mod|ui.Shift, ui.KeyZ, func() { a.history(true) })
	root := ui.Column(c.Key("workspace")).Fill().Background(worktop)
	drop := transfer.DropOptions{Formats: []transfer.Format{transfer.FileList, transfer.URIList}, Operations: transfer.Copy}
	_, dragging := ui.DataDragOver(root, drop)
	if event, ok := ui.DropData(root, drop); ok && !a.busy() {
		files, err := event.Data.Files()
		switch {
		case err != nil:
			a.status = err.Error()
		case len(files) == 0:
			a.status = "请拖入本地音视频文件"
		default:
			a.add(files)
		}
	}
	root.Children(func() {
		a.toolbar(c, narrow)
		ui.Divider(c)
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			a.library(c, dragging && !a.busy())
			ui.Divider(c)
			a.editor(c)
			if !narrow {
				ui.Divider(c)
				a.controls(c, ui.Column(c.Key("settings")).Shrink(0))
			}
		})
		ui.Divider(c)
		a.footer(c)
	})
	a.confirmRefilter(c)
}

func (a *app) toolbar(c *ui.Context, narrow bool) {
	ui.Row(c).Gap(8).Height(headerHeight).Shrink(0).Padding(0, workspaceInset).AlignItems(ui.Center).Children(func() {
		ui.Image(c, appIcon).Size(27, 27)
		ui.Text(c, title).FontSize(16).Bold()
		ui.Spacer(c)
		if narrow {
			button := textAction(c.Key("settings-button"), "过滤设置", true, func() { a.settingsOpen = !a.settingsOpen }).
				HasPopup(ui.PopupDialog).Expanded(a.settingsOpen)
			open := a.settingsOpen
			ui.Overlay(c, func() {}) // Keep the overlay parent alive for the exit animation.
			ui.PopoverBase(c.Key("settings-popover"), button, &a.settingsOpen, func(panel ui.Element) {
				panel.AttachTo(button, ui.AnchorBottomEnd, ui.AnchorTopEnd).Radius(controlRadius).
					BorderColor(border).BorderWidth(1).Focusable().AutoFocus().Label("过滤设置").
					Transition(reveal(c, &ui.Motion{Y: -4, Flip: true}, true))
				a.controls(c, panel)
			})
			if open && !a.settingsOpen {
				button.Focus()
			}
		}
		textAction(c.Key("export-clips"), "导出切片", !a.busy() && a.canExport(true), func() { a.chooseExport(true) }).
			Tooltip("将素材的各段导出为独立文件")
		primaryAction(c.Key("export-media"), "导出成片", !a.busy() && a.canExport(false), func() { a.chooseExport(false) }).
			Label("导出合并媒体").Tooltip("每个素材导出一个文件，只拼接保留片段")
	})
}

func reveal(c *ui.Context, motion *ui.Motion, exit bool) ui.ElementTransition {
	duration := revealDuration
	if c.Preferences().ReduceMotion {
		duration = -1
	}
	transition := ui.ElementTransition{Duration: duration, Enter: motion, Position: true}
	if exit {
		transition.Exit = motion
	}
	return transition
}

func (a *app) library(c *ui.Context, dragging bool) {
	panel := ui.Column(c.Key("library")).Width(sideWidth).Shrink(0).Padding(railInset).Background(rail).Gap(8)
	if dragging {
		panel.Background(hoverFace).Draw(func(p *ui.Painter, r ui.Rect) { p.Stroke(r, accent, 0, 2) })
	}
	panel.Children(func() {
		ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "素材").FontSize(14).Bold()
			if len(a.assets) > 0 {
				ui.Textf(c, "%d", len(a.assets)).TextColor(muted).FontSize(12)
			}
			ui.Spacer(c)
			iconButton(c.Key("remove"), iconRemove, "从列表移除", "移除所选素材", a.selected != nil && !a.busy(), false, a.removeSelected)
			iconButton(c.Key("add"), iconAdd, "添加音视频…", "添加音视频文件", !a.busy(), true, a.chooseFiles)
		})
		ui.Scroll(c.Key("assets")).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Gap(3).Children(func() {
				for _, item := range a.assets {
					a.assetRow(c.Key(item.path), item)
				}
			})
		})
	})
}

func (a *app) assetRow(c *ui.Context, item *asset) {
	row := ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(9, 8).Radius(controlRadius).Focusable().FocusRing(true).
		Label(item.name).Tooltip(item.path).OnClick(func() { a.selectAsset(item) }).
		Transition(reveal(c, &ui.Motion{Y: -4}, true))
	if a.selected == item {
		row.Background(hoverFace).AccessSelected(true).Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X, Y: r.Y + 9, W: 2, H: r.H - 18}, accent, 1)
		})
	}
	if row.Pressed() || a.selected != item {
		actionFace(row, true)
	}
	row.OnShortcut(0, ui.KeyEnter, func() { a.selectAsset(item) })
	row.Children(func() {
		glyph := iconAudio
		if item.info.Video {
			glyph = iconVideo
		}
		ui.Icon(c, glyph).Size(16, 16).Shrink(0).TextColor(muted)
		ui.Column(c).MinWidth(0).Grow(1).Gap(2).Children(func() {
			ui.Text(c, item.name).SingleLine().Ellipsis("…").FontSize(13)
			switch {
			case item.analyzing:
				ui.Text(c, "正在过滤…").FontSize(11).TextColor(muted)
			case item.issue != "":
				ui.Text(c, item.issue).SingleLine().Ellipsis("…").Tooltip(item.issue).FontSize(11).TextColor(waveRemovedInk)
			case item.info.Duration > 0:
				ui.Text(c, clock(item.info.Duration)).FontFeatures("tnum").FontSize(11).TextColor(muted)
			default:
				ui.Text(c, "待分析").FontSize(11).TextColor(muted)
			}
		})
	})
}

func (a *app) editor(c *ui.Context) {
	item := a.selected
	ui.Column(c.Key("editor")).Grow(1).MinWidth(0).MinHeight(0).Padding(workspaceInset).Gap(sectionGap).Children(func() {
		if item == nil {
			ui.Column(c).Fill().Center().Gap(12).Children(func() {
				ui.Icon(c, iconAudio).Size(36, 36).TextColor(accent)
				ui.Text(c, "拖入文件").FontSize(24).Bold()
				textAction(c.Key("empty-add"), "选取音视频…", !a.busy(), a.chooseFiles)
			})
			return
		}
		ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Text(c, item.name).FontSize(22).Bold().Grow(1).MinWidth(0).SingleLine().Ellipsis("…").Tooltip(item.name)
			if item.info.Video {
				ui.Textf(c, "%d × %d", item.info.Width, item.info.Height).FontSize(12).TextColor(muted)
			} else if item.info.Rate > 0 {
				ui.Textf(c, "%g kHz · %d 声道", float64(item.info.Rate)/1000, item.info.Channels).FontSize(12).TextColor(muted)
			}
		})
		a.timeline(c)
	})
}

func (a *app) previewFeedback(c *ui.Context, item *asset) {
	if a.previewIssue != "" {
		ui.Column(c).Padding(railInset).Gap(8).Center().Children(func() {
			ui.Text(c, a.previewIssue).FontSize(12).TextColor(waveRemovedInk).SingleLine().Ellipsis("…").Tooltip(a.previewIssue)
			textAction(c.Key("retry-preview"), "重试预览", !a.busy(), func() {
				a.closePreview()
				item.proxy = ""
				a.openPreview(item, false)
			})
		})
	} else {
		ui.Text(c, "正在准备预览…").FontSize(12).TextColor(muted)
	}
}

func (a *app) previewTick(c *ui.Context) {
	if a.video != nil {
		var issue string
		if err := a.video.Err(); err != nil {
			issue = err.Error()
		} else if !a.video.Ready() {
			if time.Since(a.previewSince) > 5*time.Second {
				issue = "视频预览超时"
			} else {
				c.After(100 * time.Millisecond)
			}
		} else if a.previewPlay {
			a.previewPlay = false
			a.startPlayback()
		}
		if issue != "" {
			if a.videoCut != nil {
				a.filteredVideoFailed(issue)
			} else {
				item, resume := a.selected, a.previewPlay
				a.closePreview()
				if item.proxy == "" {
					a.makePreview(item)
					a.previewPlay = resume
				} else {
					a.previewIssue = issue
				}
			}
		}
	}
	if a.updatePlaybackPosition() {
		c.After(16 * time.Millisecond)
	}
}

func (a *app) controls(c *ui.Context, panel ui.Element) {
	item, busy := a.selected, a.busy()
	enabled := item != nil && !busy
	panel.Width(settingsWidth).MinHeight(0).Padding(railInset).Gap(sectionGap).
		Background(rail).Children(func() {
		ui.Text(c, "过滤设置").Height(controlHeight).FontSize(14).Bold()
		preset := max(0, slices.Index(presets, a.opts.preset))
		tabStrip(c.Key("preset"), "剪辑节奏", &preset, presets, false, !busy, func() { a.opts.preset = presets[preset] })
		ui.Checkbox(c.Key("ends"), &a.opts.ends, "仅修剪首尾").Disabled(busy)
		ui.Divider(c)
		if a.opts.preset == "自定义" {
			_, issues := a.opts.custom()
			field := func(label, key string, value *string, issue string) {
				ui.Column(c).Gap(3).Children(func() {
					var input ui.Element
					ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
						ui.Text(c, label).Grow(1).FontSize(12)
						input = ui.TextInput(c.Key(key), value).Width(60).Label(label).Disabled(busy)
					})
					if a.validateSettings && issue != "" {
						input.ErrorMessage(ui.Text(c, issue).FontSize(11).TextColor(waveRemovedInk)).Invalid(true)
					}
				})
			}
			field("静音阈值 (dB)", "threshold", &a.opts.level, issues[0])
			field("片段前留白 (秒)", "padding-before", &a.opts.before, issues[1])
			field("片段后留白 (秒)", "padding-after", &a.opts.after, issues[2])
		} else {
			settings, _ := a.opts.settings(0)
			threshold := "—"
			if item != nil && len(item.peaks) > 0 {
				threshold = fmt.Sprintf("%.1f dB", item.automatic)
			}
			settingValue(c, "自动阈值", threshold)
			settingValue(c, "前后各留", fmt.Sprintf("%.2f 秒", float64(settings.Before)/1e6))
		}
		ui.Divider(c)
		if item != nil && len(item.history.Clips) > 0 && !busy {
			settings, err := a.opts.settings(item.automatic)
			if err != nil || settings != item.settings {
				ui.Text(c, "设置待应用").FontSize(12).TextColor(accent)
			}
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			textAction(c.Key("rerun"), "重新过滤", enabled, func() { a.requestFilter([]*asset{item}) }).
				TextColor(actionColor(enabled, true))
			textAction(c.Key("rerun-all"), "应用到全部", len(a.assets) > 1 && !busy, func() { a.requestFilter(a.assets) })
		})
		ui.Checkbox(c.Key("kept-only"), &a.onlyKept, "切片仅导出保留段").Disabled(busy)
	})
}

func (a *app) confirmRefilter(c *ui.Context) {
	open := len(a.refilter) > 0
	ui.DialogBase(c.Key("confirm-filter"), &open, func(back, panel ui.Element) {
		back.Background(screen.Alpha(.64))
		panel.Width(350).Padding(workspaceInset).Gap(sectionGap).Radius(controlRadius).Background(rail).
			Label("确认重新过滤").Transition(reveal(c, &ui.Motion{}, false))
		ui.Text(c, "重新过滤？").FontSize(16).Bold()
		ui.Textf(c, "重新过滤会覆盖 %d 个素材的手工编辑。", a.refilterCount).FontSize(13).Wrap()
		ui.Row(c).Gap(8).Children(func() {
			ui.Spacer(c)
			textAction(c.Key("keep-edits"), "取消", true, func() { a.refilter = nil }).AutoFocus()
			primaryAction(c.Key("confirm"), "重新过滤", true, func() {
				items := a.refilter
				a.refilter = nil
				if len(items) > 0 {
					a.filter(items)
				}
			})
		})
	})
	if !open {
		a.refilter = nil
	}
}

func settingValue(c *ui.Context, label, value string) {
	ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(muted)
		ui.Spacer(c)
		ui.Text(c, value).FontSize(13).FontFeatures("tnum")
	})
}

func (a *app) sampleThreshold() {
	item := a.selected
	if item == nil || len(item.peaks) == 0 {
		return
	}
	bin := min(int(a.position*int64(item.info.Rate)/timeline.Second/timeline.PeakSamples), len(item.peaks)-1)
	p := item.peaks[bin]
	amplitude := max(math.Abs(float64(p.Min)), math.Abs(float64(p.Max))) / 32768
	level := max(-70, 20*math.Log10(amplitude))
	a.opts.preset, a.opts.level = "自定义", strconv.FormatFloat(level, 'f', 1, 64)
}

func (a *app) editSpan() (timeline.Span, bool) {
	if a.selection.End > a.selection.Start {
		return a.selection, true
	}
	clips := a.selected.history.Clips
	if i := clipAt(clips, min(a.position, a.selected.info.Duration-1)); i < len(clips) {
		return clips[i].Span, true
	}
	return timeline.Span{}, false
}

func (a *app) edit(keep bool) {
	if a.selected == nil || a.busy() {
		return
	}
	if span, ok := a.editSpan(); ok {
		a.stopPlayback()
		a.selected.history.Edit(span, keep)
	}
}

func (a *app) history(redo bool) {
	if a.selected == nil || a.busy() {
		return
	}
	a.stopPlayback()
	if redo {
		a.selected.history.Redo()
	} else {
		a.selected.history.Undo()
	}
}

func (a *app) footer(c *ui.Context) {
	ui.Row(c).AlignItems(ui.Center).Gap(10).Shrink(0).Height(38).Padding(0, workspaceInset).Children(func() {
		if a.cancel != nil {
			job := "正在" + a.job
			if a.canceling {
				job = "正在取消" + a.job
			}
			ui.Text(c.Key("job-status"), job).FontSize(12).Role(ui.RoleStatus)
			ui.Progress(c, a.progress).Grow(1)
			ui.Textf(c, "%.0f%%", a.progress*100).FontSize(12).TextColor(muted)
			textAction(c.Key("cancel"), "取消", !a.canceling, a.cancelJob)
			return
		}
		ui.Text(c.Key("job-status"), a.status).Grow(1).FontSize(12).TextColor(muted).
			SingleLine().Ellipsis("…").Tooltip(a.status).Role(ui.RoleStatus)
		if a.selection.End > a.selection.Start {
			ui.Text(c, "选区 "+clock(a.selection.Start)+" – "+clock(a.selection.End)).
				FontFeatures("tnum").FontSize(12).TextColor(muted)
		}
		if a.publishedDir != "" {
			textAction(c.Key("output-folder"), "打开导出文件夹", true, func() {
				if err := mygo.Shell.OpenPath(a.publishedDir); err != nil {
					a.status = err.Error()
				}
			})
		}
	})
}

func clock(us int64) string {
	millis := us / 1000
	minutes, seconds := millis/60000, millis/1000%60
	if minutes >= 60 {
		return fmt.Sprintf("%d:%02d:%02d.%03d", minutes/60, minutes%60, seconds, millis%1000)
	}
	return fmt.Sprintf("%02d:%02d.%03d", minutes, seconds, millis%1000)
}
