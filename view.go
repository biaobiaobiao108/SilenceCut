package main

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"silencecut/internal/timeline"
)

const (
	paneGap       = 12
	paneRadius    = 8
	paneInset     = 12
	sideWidth     = 208
	settingsWidth = 238
)

var (
	worktop       = ui.Hex("#EEF1F3")
	sheet         = ui.Hex("#FFFFFF")
	border        = ui.Hex("#D8DFE3")
	ink           = ui.Hex("#1B2933")
	muted         = ui.Hex("#52616C")
	accent        = ui.Hex("#125A65")
	accentHover   = ui.Hex("#0F4C56")
	accentPressed = ui.Hex("#0D3F48")
	accentPale    = ui.Hex("#E0EEF0")
	screen        = ui.Hex("#19252F")
	screenText    = ui.Hex("#EAF0F3")
	waveBack      = ui.Hex("#F2F5F6")
	waveKeep      = ui.Hex("#DBEAE4")
	waveRemove    = ui.Hex("#F5E2DE")
	waveSelection = ui.Hex("#375866")
)

var appTheme = func() *ui.Theme {
	theme := ui.LightTheme()
	theme.Background = worktop
	theme.Surface, theme.SurfaceHover, theme.SurfacePressed = sheet, waveBack, accentPale
	theme.Border, theme.Text, theme.TextMuted = border, ink, muted
	theme.Accent, theme.AccentHover, theme.AccentPressed = accent, accentHover, accentPressed
	theme.InactiveAccent, theme.Focus, theme.Danger = accent, accent, waveRemovedInk
	theme.Radius = 6
	return theme
}()

func (a *app) view(c *ui.Context) {
	c.SetTheme(appTheme)
	a.previewTick(c)
	c.OnWindowShortcut(0, ui.KeySpace, a.togglePlay)
	mod := ui.Cmd
	c.OnWindowShortcut(mod, ui.KeyO, a.chooseFiles)
	c.OnWindowShortcut(mod, ui.KeyZ, func() { a.history(false) })
	c.OnWindowShortcut(mod|ui.Shift, ui.KeyZ, func() { a.history(true) })
	ui.Column(c).Fill().Padding(14).Gap(paneGap).Background(worktop).Children(func() {
		a.toolbar(c)
		ui.Row(c).Grow(1).MinHeight(0).Gap(paneGap).AlignItems(ui.Stretch).Children(func() {
			a.library(c)
			a.preview(c)
			a.controls(c)
		})
		a.timeline(c)
		a.footer(c)
	})
}

func (a *app) toolbar(c *ui.Context) {
	ui.Row(c).Gap(8).Height(38).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, iconSplit).Size(22, 22).TextColor(accent)
		ui.Text(c, title).FontSize(19).Bold()
		ui.Spacer(c)
		ui.Button(c.Key("export-clips"), "导出切片").Disabled(a.busy() || !a.canExport(true)).
			Tooltip("将素材的各段导出为独立文件").OnClick(func() { a.chooseExport(true) })
		ui.PrimaryButton(c.Key("export-media"), "导出合并媒体").Disabled(a.busy() || !a.canExport(false)).
			Tooltip("每个素材导出一个文件，只拼接保留片段").OnClick(func() { a.chooseExport(false) })
	})
}

func pane(e ui.Element) ui.Element {
	return e.Background(sheet).Border(1, border).Radius(paneRadius).Padding(paneInset)
}

func (a *app) library(c *ui.Context) {
	panel := pane(ui.Column(c.Key("library"))).Width(sideWidth).Shrink(0).Gap(10)
	drop := transfer.DropOptions{Formats: []transfer.Format{transfer.FileList, transfer.URIList}, Operations: transfer.Copy}
	if _, over := ui.DataDragOver(panel, drop); over && !a.busy() {
		panel.Background(accentPale).Border(1, accent)
	}
	if event, ok := ui.DropData(panel, drop); ok && !a.busy() {
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
	panel.Children(func() {
		ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "素材").FontSize(15).Bold()
			if len(a.assets) > 0 {
				ui.Textf(c, "%d", len(a.assets)).TextColor(muted).FontSize(12)
			}
			ui.Spacer(c)
			iconButton(c.Key("remove"), iconRemove, "从列表移除", "移除所选素材", a.selected != nil && !a.busy(), false, a.removeSelected)
			iconButton(c.Key("add"), iconAdd, "添加音视频…", "添加音视频文件", !a.busy(), false, a.chooseFiles)
		})
		if len(a.assets) == 0 {
			ui.Column(c).Grow(1).Center().Background(waveBack).Radius(paneRadius).Gap(8).Children(func() {
				ui.Icon(c, iconAdd).Size(22, 22).TextColor(muted)
				ui.Text(c, "拖入文件").FontSize(13).TextColor(muted)
			})
			return
		}
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
	row := ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8).Radius(6).Focusable().
		Label(item.name).Tooltip(item.path).OnClick(func() { a.selectAsset(item) })
	if a.selected == item {
		row.Background(accentPale).AccessSelected(true)
	} else if row.Pressed() {
		row.Background(accentPale)
	} else if row.Hovered() {
		row.Background(waveBack)
	}
	row.OnShortcut(0, ui.KeyEnter, func() { a.selectAsset(item) })
	row.Children(func() {
		glyph := iconAudio
		if item.info.Video {
			glyph = iconVideo
		}
		ui.Icon(c, glyph).Size(18, 18).Shrink(0).TextColor(accent)
		ui.Column(c).MinWidth(0).Grow(1).Gap(2).Children(func() {
			ui.Text(c, item.name).SingleLine().Ellipsis("…").FontSize(13)
			switch {
			case item.issue != "":
				ui.Text(c, item.issue).Wrap().FontSize(11).TextColor(waveRemovedInk)
			case item.info.Duration > 0:
				ui.Text(c, clock(item.info.Duration)).FontFeatures("tnum").FontSize(11).TextColor(muted)
			default:
				ui.Text(c, "正在分析…").FontSize(11).TextColor(muted)
			}
		})
	})
}

func (a *app) preview(c *ui.Context) {
	item := a.selected
	canvas, textColor := waveBack, muted
	if item != nil && item.info.Video {
		canvas, textColor = screen, screenText
	}
	pane(ui.Column(c)).Grow(1).MinWidth(0).Gap(10).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, "预览").FontSize(15).Bold()
			ui.Spacer(c)
			if a.video != nil && !a.video.Ready() {
				ui.Text(c, "正在打开视频…").TextColor(muted).FontSize(12)
			} else if item != nil && item.info.Video {
				ui.Textf(c, "%d × %d", item.info.Width, item.info.Height).TextColor(muted).FontSize(12)
			}
		})
		ui.Column(c).Grow(1).MinHeight(0).Radius(6).Background(canvas).Clip().Center().Children(func() {
			switch {
			case a.video != nil:
				ui.Video(c, a.video).Fill().Fit(ui.Contain)
			case a.previewCancel != nil:
				ui.Text(c, "正在准备预览…").TextColor(textColor)
			case a.previewIssue != "":
				ui.Column(c).Padding(14).Gap(8).Center().Children(func() {
					ui.Text(c, a.previewIssue).TextColor(textColor).Wrap()
					ui.Button(c.Key("retry-preview"), "重试预览").Disabled(a.busy()).OnClick(func() {
						a.closePreview()
						item.proxy = ""
						a.openPreview(item, false)
					})
				})
			case item != nil && item.info.Duration > 0:
				ui.Column(c).Padding(16).Gap(8).Center().Children(func() {
					ui.Box(c).Size(50, 50).Radius(12).Background(accentPale).Center().Children(func() {
						ui.Icon(c, iconAudio).Size(26, 26).TextColor(accent)
					})
					ui.Text(c, item.name).FontSize(15).Bold().MaxLines(2).Ellipsis("…")
					ui.Textf(c, "%g kHz  ·  %d 声道", float64(item.info.Rate)/1000, item.info.Channels).
						TextColor(muted).FontSize(12)
				})
			case item != nil && a.cancel != nil:
				ui.Text(c, "正在分析素材…").TextColor(textColor)
			default:
				ui.Text(c, "等待素材").TextColor(textColor)
			}
		})
	})
}

func (a *app) previewTick(c *ui.Context) {
	if a.video != nil {
		if err := a.video.Err(); err != nil {
			item := a.selected
			a.closePreview()
			if item.proxy == "" {
				a.makePreview(item)
			} else {
				a.previewIssue = err.Error()
			}
		} else if !a.video.Ready() {
			if time.Since(a.previewSince) > 5*time.Second {
				item := a.selected
				a.closePreview()
				if item.proxy == "" {
					a.makePreview(item)
				} else {
					a.previewIssue = "视频预览超时"
				}
			} else {
				c.After(100 * time.Millisecond)
			}
		}
	}
	if a.updatePlaybackPosition() {
		c.After(16 * time.Millisecond)
	}
}

func (a *app) controls(c *ui.Context) {
	item := a.selected
	busy := a.busy()
	pane(ui.Column(c)).Width(settingsWidth).Shrink(0).Gap(9).Children(func() {
		ui.Text(c, "过滤设置").FontSize(15).Bold()
		ui.Row(c).Gap(3).Children(func() {
			for _, preset := range presets {
				button := ui.Button
				if a.opts.preset == preset {
					button = ui.PrimaryButton
				}
				width := float32(44)
				if preset == "自定义" {
					width = 63
				}
				button(c.Key("preset-"+preset), preset).Width(width).FontSize(12).Padding(5, 4).Disabled(busy).
					OnClick(func() { a.opts.preset = preset })
			}
		})
		ui.Checkbox(c.Key("ends"), &a.opts.ends, "仅修剪首尾").Disabled(busy)
		if a.opts.preset == "自定义" {
			field := func(label, key string, value *string) {
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					ui.Text(c, label).Grow(1).FontSize(12)
					ui.TextInput(c.Key(key), value).Width(62).Label(label).Disabled(busy)
				})
			}
			field("静音阈值 (dB)", "threshold", &a.opts.level)
			field("片段前留白 (秒)", "padding-before", &a.opts.before)
			field("片段后留白 (秒)", "padding-after", &a.opts.after)
		} else {
			settings, _ := a.opts.settings(0)
			if item != nil && len(item.peaks) > 0 {
				ui.Textf(c, "自动阈值  %.1f dB", item.automatic).TextColor(muted).FontSize(12)
			} else {
				ui.Text(c, "按素材估算阈值").TextColor(muted).FontSize(12)
			}
			ui.Textf(c, "前后各留 %.2f 秒", float64(settings.Before)/1e6).TextColor(muted).FontSize(12)
		}
		ui.Spacer(c)
		if item != nil && len(item.history.Clips) > 0 && !busy {
			settings, err := a.opts.settings(item.automatic)
			if err != nil || settings != item.settings {
				ui.Text(c, "设置待应用").FontSize(12).TextColor(accent)
			}
		}
		ui.Row(c).Gap(6).Children(func() {
			ui.Button(c.Key("rerun"), "重新过滤").Grow(1).Disabled(item == nil || busy).
				OnClick(func() { a.filter([]*asset{item}) })
			ui.Button(c.Key("rerun-all"), "应用到全部").Disabled(len(a.assets) < 2 || busy).
				OnClick(func() { a.filter(a.assets) })
		})
		ui.Checkbox(c.Key("kept-only"), &a.onlyKept, "切片仅导出保留段").Disabled(busy)
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
	ui.Row(c).AlignItems(ui.Center).Gap(10).Height(27).Children(func() {
		if a.cancel != nil {
			ui.Text(c, a.job).FontSize(12)
			ui.Progress(c, a.progress).Grow(1)
			ui.Textf(c, "%.0f%%", a.progress*100).FontSize(12).TextColor(muted)
			ui.Button(c.Key("cancel"), "取消").OnClick(a.cancel)
			return
		}
		ui.Text(c, a.status).Grow(1).FontSize(12).TextColor(muted).SingleLine().Ellipsis("…").Tooltip(a.status)
		if a.outputDir != "" {
			ui.Button(c.Key("output-folder"), "打开导出文件夹").OnClick(func() {
				if err := mygo.Shell.OpenPath(a.outputDir); err != nil {
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
