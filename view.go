package main

import (
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"silencecut/internal/timeline"
)

const (
	workspaceInset   = 20
	railInset        = 14
	sideWidth        = 216
	settingsWidth    = 272
	sectionGap       = 12
	controlHeight    = 34
	controlRadius    = 8
	surfaceRadius    = 12
	overlayRadius    = 16
	topBarHeight     = 46
	wideWorkspace    = 1120
	hoverDuration    = 100 * time.Millisecond
	switchDuration   = 160 * time.Millisecond
	revealDuration   = 180 * time.Millisecond
	windowBackground = "#161A1C"
)

var (
	worktop       = ui.Hex(windowBackground)
	rail          = ui.Hex("#1A1F21")
	surface       = ui.Hex("#202527")
	raised        = ui.Hex("#262C2E")
	border        = ui.Hex("#34383B")
	ink           = ui.Hex("#F3F1EA")
	muted         = ui.Hex("#A9B0B3")
	subtle        = ui.Hex("#8D9599")
	disabledInk   = ui.Hex("#6F767A")
	accent        = ui.Hex("#F3BD66")
	accentHover   = ui.Hex("#F8CE88")
	accentPressed = ui.Hex("#DDA552")
	hoverFace     = ui.Hex("#24292B")
	pressedFace   = ui.Hex("#2D3335")
	screen        = ui.Hex("#111416")
	waveBack      = ui.Hex("#1E2325")
	waveKeep      = ui.Hex("#22302C")
	waveRemove    = ui.Hex("#342427")
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
	a.recoveryTick(c)
	c.OnWindowShortcut(0, ui.KeySpace, a.togglePlay)
	mod := ui.Cmd
	c.OnWindowShortcut(mod, ui.KeyO, a.chooseFiles)
	c.OnWindowShortcut(mod|ui.Shift, ui.KeyO, a.chooseOpenProject)
	c.OnWindowShortcut(mod, ui.KeyS, func() { a.chooseSaveProject(nil) })
	c.OnWindowShortcut(mod, ui.KeyZ, func() { a.history(false) })
	c.OnWindowShortcut(mod|ui.Shift, ui.KeyZ, func() { a.history(true) })
	root := ui.Column(c.Key("workspace")).Fill()
	if c.Vibrancy() {
		root.Background(ui.Transparent)
	} else {
		root.Background(worktop)
	}
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
		a.workspace(c, narrow, dragging && !a.busy())
		a.toolbar(c, narrow)
		if dragging && !a.busy() {
			a.dropOverlay(c)
		}
	})
	a.confirmRefilter(c)
}

func (a *app) workspace(c *ui.Context, narrow, dragging bool) {
	ui.Row(c.Key("workspace-body")).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		a.library(c, dragging)
		ui.Divider(c)
		a.editor(c, dragging)
		if !narrow {
			ui.Divider(c)
			panel := ui.Column(c.Key("settings")).Shrink(0).MinHeight(0).Width(settingsWidth)
			if !c.Preferences().ReduceMotion {
				panel.Transition(ui.ElementTransition{Duration: revealDuration, Width: true, Position: true})
			}
			a.controls(c, panel, settingsWidth, topBarHeight+4)
		}
	})
}

func (a *app) toolbar(c *ui.Context, narrow bool) {
	bar := c.TitleBar()
	topbar := ui.Row(c.Key("topbar")).Absolute().Top(0).Left(0).Right(0).Height(topBarHeight).
		AlignItems(ui.Stretch).DragWindow()
	topbar.Children(func() {
		ui.Row(c.Key("topbar-project")).Width(sideWidth).Shrink(0).Height(topBarHeight).
			Padding(0, railInset, 0, float32(workspaceInset)+bar.Left).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Image(c, appIcon).Size(22, 22).Shrink(0)
			if name := a.projectName(); name != "" {
				ui.Text(c, name).FontSize(14).FontWeight(600).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			} else {
				ui.Spacer(c).Grow(1)
			}
			if a.projectDirty {
				ui.Text(c, "未保存").FontSize(11).TextColor(subtle).Shrink(0)
			}
		})
		right := float32(workspaceInset) + bar.Right
		if !narrow {
			right = railInset
		}
		ui.Row(c.Key("topbar-command")).Grow(1).MinWidth(0).Height(topBarHeight).
			Padding(0, right, 0, railInset).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Gap(1).Shrink(0).Children(func() {
				iconButton(c.Key("project-open"), iconOpen, "打开项目", !a.busy(), false, a.chooseOpenProject)
				iconButton(c.Key("project-save"), iconSave, "保存项目",
					!a.busy() && (a.projectDirty || a.projectPath != "" || len(a.assets) > 0),
					false, func() { a.chooseSaveProject(nil) })
			})
			ui.Spacer(c)
			if narrow {
				button := iconButton(c.Key("settings-button"), iconSliders, "过滤设置", true, a.settingsOpen,
					func() { a.settingsOpen = !a.settingsOpen }).AccessSelected(a.settingsOpen)
				if a.settingsOpen {
					button.Background(raised)
				}
				button.HasPopup(ui.PopupDialog).Expanded(a.settingsOpen)
				open := a.settingsOpen
				ui.Overlay(c, func() {}) // Keep the overlay parent alive for the exit animation.
				ui.PopoverBase(c.Key("settings-popover"), button, &a.settingsOpen, func(panel ui.Element) {
					panel.AttachTo(button, ui.AnchorBottomEnd, ui.AnchorTopEnd).Width(settingsWidth).
						Radius(surfaceRadius).BorderColor(border).BorderWidth(1).Focusable().AutoFocus().Label("过滤设置").
						Transition(reveal(c, &ui.Motion{Y: -4, Flip: true}, true))
					a.controls(c, panel, settingsWidth, railInset)
				})
				if open && !a.settingsOpen {
					button.Focus()
				}
			}
			primaryAction(c.Key("export-media"), "导出成片", !a.busy() && a.canExport(), a.chooseExport).
				Label("导出合并媒体").Description("每个素材导出一个文件，只拼接保留片段")
		})
		if !narrow {
			ui.Box(c.Key("topbar-inspector")).Width(settingsWidth + 1).Shrink(0)
		}
	})
}

func (a *app) projectName() string {
	if a.projectPath != "" {
		return filepath.Base(a.projectPath)
	}
	if len(a.assets) > 0 {
		return "未命名项目"
	}
	return ""
}

func (a *app) dropOverlay(c *ui.Context) {
	ui.Box(c.Key("drop-overlay")).Absolute().Top(0).Left(0).Right(0).Bottom(0).
		Background(screen.Alpha(.76)).PassThrough().Center().Children(func() {
		ui.Column(c).Width(390).Padding(26).Gap(10).Center().Radius(overlayRadius).
			Background(raised).BorderColor(accent).BorderWidth(1).PassThrough().Children(func() {
			ui.Icon(c, iconAdd).Size(28, 28).TextColor(accent)
			ui.Text(c, "松开以添加音视频").FontSize(18).FontWeight(600)
			ui.Text(c, "文件会加入当前工作区").FontSize(12).TextColor(subtle)
		})
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
	panel := ui.Column(c.Key("library")).Width(sideWidth).Shrink(0).Clip().
		Padding(topBarHeight+4, railInset, railInset, railInset).Gap(8)
	if !c.Vibrancy() {
		panel.Background(rail)
	}
	if dragging {
		panel.Background(hoverFace.Alpha(.72)).Draw(func(p *ui.Painter, r ui.Rect) { p.Stroke(r, accent, 0, 2) })
	}
	panel.Children(func() {
		ui.Row(c).Height(34).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "素材").FontSize(15).FontWeight(600)
			if len(a.assets) > 0 {
				ui.Textf(c, "%d", len(a.assets)).TextColor(subtle).FontSize(11).FontFeatures("tnum")
			}
			ui.Spacer(c)
			iconButton(c.Key("remove"), iconRemove, "从列表移除", a.selected != nil && !a.busy(), false, a.removeSelected)
			iconButton(c.Key("add"), iconAdd, "添加音视频…", !a.busy(), true, a.chooseFiles)
		})
		ui.Scroll(c.Key("assets")).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				for _, item := range a.assets {
					a.assetRow(c.Key(item), item)
				}
			})
		})
	})
}

func (a *app) assetRow(c *ui.Context, item *asset) {
	row := ui.Row(c).Height(52).Gap(8).AlignItems(ui.Center).Padding(0, 8).Radius(controlRadius).Focusable().FocusRing(true).
		Label(item.name).Description(item.path).OnClick(func() { a.selectAsset(item) }).
		Transition(reveal(c, &ui.Motion{Y: -4}, true))
	if a.selected == item {
		row.Background(surface).AccessSelected(true).Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X, Y: r.Y + 10, W: 2, H: r.H - 20}, accent, 1)
		})
	}
	if row.Pressed() || a.selected != item {
		actionFace(c, row, true)
	}
	row.OnShortcut(0, ui.KeyEnter, func() { a.selectAsset(item) })
	row.Children(func() {
		glyph := iconAudio
		if item.info.Video {
			glyph = iconVideo
		}
		ui.Icon(c, glyph).Size(17, 17).Shrink(0).TextColor(subtle)
		ui.Column(c).MinWidth(0).Grow(1).Gap(3).Children(func() {
			ui.Text(c, item.name).SingleLine().Ellipsis("…").FontSize(13).FontWeight(500)
			switch {
			case item.analyzing:
				ui.Text(c, "正在过滤…").FontSize(11).TextColor(subtle)
			case item.issue != "":
				ui.Text(c, item.issue).SingleLine().Ellipsis("…").FontSize(11).TextColor(waveRemovedInk)
			case item.exportIssue != "":
				ui.Text(c, "导出失败 · "+item.exportIssue).SingleLine().Ellipsis("…").
					FontSize(11).TextColor(waveRemovedInk)
			case item.info.Duration > 0:
				ui.Text(c, clock(item.info.Duration)).FontFeatures("tnum").FontSize(11).TextColor(subtle)
			default:
				ui.Text(c, "待分析").FontSize(11).TextColor(subtle)
			}
		})
	})
}

func (a *app) editor(c *ui.Context, dragging bool) {
	item := a.selected
	editor := ui.Column(c.Key("editor")).Grow(1).MinWidth(0).MinHeight(0).Background(worktop).
		Padding(topBarHeight+4, workspaceInset, 16, workspaceInset).Gap(sectionGap)
	editor.Children(func() {
		a.activity(c)
		if item == nil {
			a.emptyEditor(c, dragging)
			return
		}
		ui.Row(c).Height(30).AlignItems(ui.Center).Gap(10).Children(func() {
			glyph := iconAudio
			if item.info.Video {
				glyph = iconVideo
			}
			ui.Icon(c, glyph).Size(17, 17).TextColor(subtle).Shrink(0)
			ui.Text(c, item.name).FontSize(18).FontWeight(600).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			if item.offline && !item.pendingLoad {
				textAction(c.Key("relink-source"), "重新定位…", !a.busy(), func() { a.relinkProjectSource(item) })
			} else if item.info.Video {
				ui.Textf(c, "%d × %d", item.info.Width, item.info.Height).FontSize(12).TextColor(subtle).FontFeatures("tnum")
			} else if item.info.Rate > 0 {
				ui.Textf(c, "%g kHz · %d 声道", float64(item.info.Rate)/1000, item.info.Channels).FontSize(12).TextColor(subtle)
			}
		})
		a.timeline(c)
	})
}

func (a *app) emptyEditor(c *ui.Context, dragging bool) {
	ui.Column(c.Key("empty-workspace")).Grow(1).MinHeight(0).Center().Children(func() {
		drop := ui.Column(c).Width(470).Padding(24).Gap(12).Center().Radius(overlayRadius).
			Background(surface).BorderColor(border).BorderWidth(1)
		if dragging {
			drop.Background(raised).BorderColor(accent)
		}
		drop.Children(func() {
			ui.Icon(c, iconAudio).Size(32, 32).TextColor(accent)
			ui.Text(c, "拖入音视频").FontSize(24).FontWeight(600)
			ui.Text(c, "音频和视频会在本机完成过滤与处理").FontSize(13).TextColor(muted).TextAlign(ui.Center)
			primaryAction(c.Key("empty-add"), "选择音视频…", !a.busy(), a.chooseFiles).Margin(4, 0, 0, 0)
		})
	})
}

func (a *app) previewFeedback(c *ui.Context, item *asset) {
	if a.previewIssue == "" {
		ui.Text(c, "正在打开原片…").FontSize(12).TextColor(muted).Role(ui.RoleStatus)
		return
	}
	ui.Column(c).Padding(railInset).Gap(8).Center().Children(func() {
		ui.Text(c, a.previewIssue).FontSize(12).TextColor(waveRemovedInk).SingleLine().Ellipsis("…")
		textAction(c.Key("retry-preview"), "重试预览", !a.busy(), func() {
			a.closePreview()
			a.openPreview(item)
		})
	})
}

func (a *app) previewTick(c *ui.Context) {
	if a.video != nil {
		var issue string
		if err := a.video.Err(); err != nil {
			issue = err.Error()
		} else if !a.video.Ready() {
			if time.Since(a.previewSince) > 5*time.Second {
				issue = "打开原片超时"
			} else {
				c.After(100 * time.Millisecond)
			}
		} else if !a.previewSince.IsZero() {
			a.previewSince = time.Time{}
			if a.position > 0 {
				a.seek(a.position)
			}
		}
		if issue != "" {
			a.closePreview()
			a.previewIssue = "无法播放原片 · " + issue
		}
	} else if preview, ok := a.player.(interface{ Err() error }); ok {
		if err := preview.Err(); err != nil {
			a.closePreview()
			a.previewIssue = err.Error()
		}
	}
	if a.updatePlaybackPosition() {
		c.After(16 * time.Millisecond)
	}
}

func (a *app) controls(c *ui.Context, panel ui.Element, width, top float32) {
	item, busy := a.selected, a.busy()
	enabled := item != nil && !busy
	panel.Width(width).MinHeight(0).Padding(top, railInset, railInset, railInset).Gap(sectionGap).
		Background(surface).Children(func() {
		ui.Row(c).Height(34).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Icon(c, iconSliders).Size(17, 17).TextColor(subtle)
			ui.Text(c, "过滤设置").FontSize(15).FontWeight(600)
		})
		preset := max(0, slices.Index(presets, a.opts.preset))
		tabStrip(c.Key("preset"), "剪辑节奏", &preset, presets, false, !busy, func() {
			a.opts.preset = presets[preset]
			a.markProject()
		})
		ui.Checkbox(c.Key("ends"), &a.opts.ends, "仅修剪首尾").Disabled(busy).OnChange(a.markProject)
		ui.Divider(c)
		if a.opts.preset == "自定义" {
			_, issues := a.opts.custom()
			field := func(label, key string, value *string, issue string) {
				ui.Column(c).Gap(4).Children(func() {
					var input ui.Element
					ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
						ui.Text(c, label).Grow(1).FontSize(12).TextColor(muted)
						input = ui.TextInput(c.Key(key), value).Width(68).Label(label).Disabled(busy).OnChange(a.markProject)
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
			settingValue(c, "最短停顿", fmt.Sprintf("%d 毫秒", settings.MinPause/1000))
			settingValue(c, "前 / 后留白", fmt.Sprintf("%d / %d 毫秒", settings.Before/1000, settings.After/1000))
		}
		ui.Divider(c)
		pending := false
		if item != nil && len(item.history.Clips) > 0 && !busy {
			settings, err := a.opts.settings(item.automatic)
			pending = err != nil || settings != item.settings
		}
		rerunLabel := "重新过滤"
		if pending {
			rerunLabel = "应用设置"
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			textAction(c.Key("rerun"), rerunLabel, enabled, func() { a.requestFilter([]*asset{item}) }).
				TextColor(actionColor(enabled, true))
			textAction(c.Key("rerun-all"), "应用到全部", len(a.assets) > 1 && !busy, func() { a.requestFilter(a.assets) })
		})
	})
}

func (a *app) confirmRefilter(c *ui.Context) {
	open := len(a.refilter) > 0
	ui.DialogBase(c.Key("confirm-filter"), &open, func(back, panel ui.Element) {
		back.Background(screen.Alpha(.64))
		panel.Width(360).Padding(24).Gap(sectionGap).Radius(overlayRadius).Background(raised).BorderColor(border).BorderWidth(1).
			Label("确认重新过滤").Transition(reveal(c, &ui.Motion{}, false))
		ui.Text(c, "重新过滤？").FontSize(18).FontWeight(600)
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
	ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
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
	amplitude := p.Magnitude()
	level := max(-70, 20*math.Log10(amplitude))
	a.opts.preset, a.opts.level = "自定义", strconv.FormatFloat(level, 'f', 1, 64)
	a.markProject()
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
		a.markProject()
	}
}

func (a *app) history(redo bool) {
	if a.selected == nil || a.busy() {
		return
	}
	a.stopPlayback()
	if redo && a.selected.history.CanRedo() {
		a.selected.history.Redo()
	} else if !redo && a.selected.history.CanUndo() {
		a.selected.history.Undo()
	} else {
		return
	}
	a.markProject()
}

func (a *app) activity(c *ui.Context) {
	pending := slices.ContainsFunc(a.assets, func(item *asset) bool { return item.pendingLoad })
	if a.cancel == nil && a.status == "" && a.publishedDir == "" && !pending {
		return
	}
	ui.Row(c.Key("activity")).AlignItems(ui.Center).Gap(8).Shrink(0).Height(controlHeight).Children(func() {
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
		if a.status != "" {
			ui.Text(c.Key("job-status"), a.status).Grow(1).FontSize(12).TextColor(muted).
				SingleLine().Ellipsis("…").Role(ui.RoleStatus)
		} else {
			ui.Spacer(c).Grow(1)
		}
		if pending {
			textAction(c.Key("resume-sources"), "继续读取素材", !a.busy(), a.resumeProjectSources)
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
