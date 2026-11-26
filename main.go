package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

const title = "极速过滤"

var fileTypes = []string{"mp4", "mov", "m4v", "mkv", "webm", "avi", "mpg", "mpeg", "ts", "m2ts", "flv", "wmv", "wav", "mp3", "m4a", "aac", "flac", "ogg", "opus", "aif", "aiff", "caf", "wma", "ac3"}
var presets = []string{"慢", "中等", "快", "自定义"}

type filterOptions struct {
	preset, level, before, after string
	ends                         bool
}

func (f filterOptions) settings(automatic float64) (timeline.Settings, error) {
	s := timeline.Settings{Threshold: automatic, OnlyEnds: f.ends}
	switch f.preset {
	case "慢":
		s.MinPause, s.Before, s.After, s.BreathMargin = 280_000, 100_000, 120_000, 10
	case "中等":
		s.MinPause, s.Before, s.After, s.BreathMargin = 200_000, 65_000, 80_000, 7
	case "快":
		s.MinPause, s.Before, s.After, s.BreathMargin = 140_000, 35_000, 50_000, 4
	case "自定义":
		values, issues := f.custom()
		for _, issue := range issues {
			if issue != "" {
				return s, errors.New(issue)
			}
		}
		s.Threshold = values[0]
		s.Before, s.After = int64(math.Round(values[1]*1e6)), int64(math.Round(values[2]*1e6))
	}
	return s, nil
}

func (f filterOptions) custom() (values [3]float64, issues [3]string) {
	for i, field := range [...]struct {
		text, issue string
		min, max    float64
	}{
		{f.level, "请输入 −70 到 20 dB", -70, 20},
		{f.before, "请输入 0 到 10 秒", 0, 10},
		{f.after, "请输入 0 到 10 秒", 0, 10},
	} {
		value, err := strconv.ParseFloat(strings.TrimSpace(field.text), 64)
		if err != nil || math.IsNaN(value) || value < field.min || value > field.max {
			issues[i] = field.issue
		}
		values[i] = value
	}
	return
}

type asset struct {
	path, name  string
	file        os.FileInfo
	info        media.Info
	peaks       []timeline.Peak
	waveform    timeline.Waveform
	pendingWave *analysisWave
	automatic   float64
	settings    timeline.Settings
	history     timeline.History
	filtered    []timeline.Clip
	analyzing   bool
	issue       string
	exportIssue string
	source      sourceStamp
	offline     bool
	pendingLoad bool
}

type analysisWave struct {
	info media.Info
	wave timeline.Waveform
}

type player interface {
	Play()
	Pause()
	Playing() bool
	Position() time.Duration
	Seek(time.Duration)
	Close()
}

type jobState struct {
	cancel                         context.CancelFunc
	job, status, outputDir         string
	publishedDir                   string
	progress                       float64
	picking, closing, canceling    bool
	settingsOpen, validateSettings bool
	refilter                       []*asset
	refilterCount                  int
}

type app struct {
	win      *mygo.Window
	workers  sync.WaitGroup
	assets   []*asset
	selected *asset
	opts     filterOptions
	jobState
	previewState
	timelineState
	projectState
}

func main() {
	state := &app{
		opts: filterOptions{preset: "中等", level: "-40", before: "0.10", after: "0.10"},
	}
	recovery, previous, recoveryErr := openRecovery()
	state.recovery = recovery
	if recoveryErr != nil {
		state.status = "无法启动项目恢复 · " + recoveryErr.Error()
	}
	defer state.recovery.close(false)
	mygo.App.WhenReady(func() {
		mygo.Theme.SetSource(mygo.ThemeDark)
		state.win = mygo.NewWindow(mygo.WindowOptions{
			Title: title, Width: 1180, Height: 790, MinWidth: 930, MinHeight: 690,
			TitleBarStyle:        mygo.TitleBarHidden,
			TitleBarHeight:       topBarHeight,
			TrafficLightPosition: &mygo.Point{X: 18, Y: 16},
			Vibrancy:             mygo.VibrancySidebar,
			BackgroundColor:      windowBackground,
			Content:              ui.View(state.view),
		})
		state.win.SetWindowButtonsTheme(mygo.ThemeDark)
		if runtime.GOOS == "windows" {
			var icon bytes.Buffer
			if err := png.Encode(&icon, appIcon.Image(128, 128, nil)); err != nil {
				log.Print(err)
			} else if err := state.win.SetIcon(icon.Bytes()); err != nil {
				log.Print(err)
			}
		}
		state.win.OnClose(func(e *mygo.CloseEvent) {
			if !state.requestClose() {
				e.PreventDefault()
			}
		})
		state.win.OnClosed(func() {
			state.closing = true
			if state.closeApproved || !state.projectDirty {
				state.clearRecovery()
			} else if err := state.writeRecovery(); err != nil {
				log.Print(err)
			}
			state.closePreview()
			if state.cancel != nil {
				state.cancel()
			}
		})
		switch {
		case len(os.Args) > 1:
			state.add(os.Args[1:])
		case previous != nil:
			state.offerRecovery(previous)
		}
	})
	err := mygo.App.Run()
	previous.close(false)
	state.workers.Wait()
	if err != nil {
		log.Fatal(err)
	}
}

func (a *app) requestClose() bool {
	if a.picking {
		return false
	}
	if !a.projectDirty || a.closeApproved {
		return true
	}
	if a.cancel != nil {
		a.closePending = true
		a.cancelJob()
		return false
	}
	a.refilter = nil
	a.guardProject(func() {
		a.closeApproved = true
		a.win.Close()
	})
	return false
}

func (a *app) async(fn func()) {
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		fn()
	}()
}

func (a *app) busy() bool { return a.cancel != nil || a.picking || len(a.refilter) > 0 }

func (a *app) chooseFiles() {
	if a.busy() {
		return
	}
	a.pick(mygo.OpenDialogOptions{
		Parent: a.win, Title: "添加音视频", Multiple: true,
		Filters: []mygo.FileFilter{{Name: "音视频", Extensions: fileTypes}, {Name: "所有文件", Extensions: []string{"*"}}},
	}, a.add)
}

func (a *app) pick(options mygo.OpenDialogOptions, done func([]string)) {
	a.picking = true
	a.async(func() {
		paths, err := mygo.Dialog.Open(options)
		a.win.Update(func() {
			a.picking = false
			if a.closing {
				return
			}
			if err != nil {
				a.status = err.Error()
			} else if len(paths) > 0 {
				done(paths)
			}
		})
	})
}

func (a *app) add(paths []string) {
	if a.busy() || len(paths) == 0 {
		return
	}
	if slices.ContainsFunc(paths, isProject) {
		if len(paths) == 1 {
			a.openProject(paths[0])
		} else {
			a.status = "请一次打开一个项目"
		}
		return
	}
	if !a.validSettings() {
		return
	}
	var items []*asset
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			a.status = err.Error()
			continue
		}
		file, statErr := os.Stat(abs)
		if slices.ContainsFunc(a.assets, func(item *asset) bool {
			return item.path == abs || statErr == nil && item.file != nil && os.SameFile(file, item.file)
		}) {
			continue
		}
		item := &asset{path: abs, name: filepath.Base(abs), file: file}
		a.assets = append(a.assets, item)
		items = append(items, item)
	}
	if len(items) == 0 {
		a.status = "所选素材已在列表中"
		return
	}
	a.markProject()
	if a.selected == nil {
		a.selectAsset(items[0])
	}
	a.filter(items)
}

func (a *app) selectAsset(item *asset) {
	if a.selected == item {
		return
	}
	a.closePreview()
	a.selected = item
	a.position, a.viewStart, a.viewLength = 0, 0, 0
	a.selection = timeline.Span{}
	a.waveDrag, a.overviewDrag = waveGesture{}, overviewGesture{}
	a.scheduleRecovery()
	if item != nil && !item.offline && item.info.Duration > 0 {
		a.openPreview(item)
	}
}

func (a *app) removeSelected() {
	a.removeAsset(a.selected)
}

func (a *app) removeAsset(item *asset) {
	if item == nil || a.busy() {
		return
	}
	index := slices.Index(a.assets, item)
	if index < 0 {
		return
	}
	a.assets = slices.Concat(a.assets[:index], a.assets[index+1:])
	if len(a.assets) == 0 {
		a.selectAsset(nil)
	} else {
		a.selectAsset(a.assets[min(index, len(a.assets)-1)])
	}
	a.markProject()
	a.status = ""
}

func (a *app) begin(job string) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel, a.job, a.status, a.progress = cancel, job, "", 0
	a.canceling = false
	a.stopPlayback()
	return ctx
}

func (a *app) cancelJob() {
	if a.cancel == nil || a.canceling {
		return
	}
	a.canceling = true
	a.cancel()
}

func (a *app) end(status string) {
	a.cancel()
	a.cancel, a.job, a.status = nil, "", status
	a.canceling = false
	if a.closePending {
		a.closePending = false
		a.win.Close()
	}
}

func (a *app) report() media.Progress {
	last := time.Time{}
	return func(value float64) {
		if value < 1 && time.Since(last) < 80*time.Millisecond {
			return
		}
		last = time.Now()
		a.win.Update(func() {
			if !a.closing {
				a.progress = max(a.progress, value)
			}
		})
	}
}

func (a *app) validSettings() bool {
	_, err := a.opts.settings(0)
	a.validateSettings = err != nil
	if a.validateSettings {
		a.settingsOpen = true
		a.status = "请修正过滤设置 · " + err.Error()
	}
	return !a.validateSettings
}

func (a *app) requestFilter(items []*asset) {
	if a.busy() || len(items) == 0 || !a.validSettings() {
		return
	}
	count := 0
	for _, item := range items {
		if item.filtered != nil && !slices.Equal(item.filtered, item.history.Clips) {
			count++
		}
	}
	if count > 0 {
		a.settingsOpen = false
		a.refilter, a.refilterCount = slices.Clone(items), count
		return
	}
	a.filter(items)
}

func (a *app) filter(items []*asset) {
	opts := a.opts
	ctx := a.begin("过滤")
	work := make([]asset, 0, len(items))
	for _, item := range items {
		work = append(work, *item)
		item.analyzing = true
	}
	a.async(func() {
		report := a.report()
		var succeeded, failed int
		for index, current := range work {
			if ctx.Err() != nil {
				break
			}
			item := items[index]
			update := func(value float64) { report((float64(index) + value) / float64(len(items))) }
			var preview func(media.Info, []timeline.Peak)
			if current.info.Duration == 0 {
				preview = func(info media.Info, peaks []timeline.Peak) {
					wave := &analysisWave{info: info, wave: timeline.BuildWaveform(peaks)}
					a.win.Update(func() {
						if !a.closing && ctx.Err() == nil && item.analyzing {
							item.pendingWave = wave
						}
					})
				}
			}
			err := current.filter(ctx, opts, update, preview)
			if err != nil && ctx.Err() != nil {
				break
			}
			update(1)
			if err != nil {
				failed++
				a.win.Update(func() {
					if a.closing {
						return
					}
					item.analyzing = false
					item.pendingWave = nil
					item.issue = err.Error()
				})
				continue
			}
			succeeded++
			a.win.Update(func() {
				if a.closing {
					return
				}
				reload := item.info.Duration == 0 || item.offline || changed(item.file, current.file)
				if reload {
					if item == a.selected {
						a.closePreview()
					}
				}
				item.info, item.peaks, item.automatic = current.info, current.peaks, current.automatic
				item.waveform = current.waveform
				item.pendingWave = nil
				item.file, item.settings, item.history = current.file, current.settings, current.history
				item.filtered, item.analyzing, item.issue, item.exportIssue = current.filtered, false, "", ""
				item.offline = false
				item.pendingLoad = false
				a.markProject()
				if item == a.selected {
					a.selection = timeline.Span{}
					if reload {
						a.position, a.viewStart, a.viewLength = 0, 0, 0
						a.openPreview(item)
					}
				}
			})
		}
		status := batchStatus("过滤", fmt.Sprintf("已过滤 %d 个素材", succeeded), succeeded, failed, "可查看错误详情", ctx.Err() != nil)
		a.win.Update(func() {
			if a.closing {
				return
			}
			for _, item := range items {
				item.analyzing = false
				item.pendingWave = nil
			}
			a.end(status)
		})
	})
}

func (item *asset) filter(ctx context.Context, opts filterOptions, update media.Progress, preview func(media.Info, []timeline.Peak)) error {
	before, err := os.Stat(item.path)
	if err != nil {
		return err
	}
	settings, err := opts.settings(item.automatic)
	if err != nil {
		return err
	}
	reload := item.info.Duration == 0 || changed(item.file, before)
	var analysis media.AudioAnalysis
	var clips []timeline.Clip
	info := item.info
	if reload {
		analysis, err = media.AnalyzeAudio(ctx, item.path, settings, update, preview)
		info, clips = analysis.Info, analysis.Clips
		if settings.MinPause > 0 {
			settings.Threshold = analysis.Automatic
		}
	} else {
		clips, err = media.Detect(ctx, item.path, settings, update)
	}
	if err != nil {
		return err
	}
	after, err := os.Stat(item.path)
	if err != nil {
		return err
	}
	if changed(before, after) || !validProjectClips(clips, info.Duration) {
		return errors.New("素材在过滤期间已改变，请重新过滤")
	}
	var wave timeline.Waveform
	if reload {
		wave = timeline.BuildWaveform(analysis.Peaks)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if reload {
		item.info, item.peaks, item.automatic = info, analysis.Peaks, analysis.Automatic
		item.waveform = wave
	}
	item.file, item.settings, item.filtered = after, settings, clips
	item.history = timeline.History{Clips: clips}
	return nil
}

func batchStatus(action, completed string, succeeded, failed int, issue string, canceled bool) string {
	switch {
	case canceled:
		completed = "已取消" + action + " · " + completed
	case failed > 0 && succeeded > 0:
		completed = action + "部分成功 · " + completed
	case failed > 0:
		completed = action + "失败 · " + completed
	}
	if failed > 0 {
		completed += fmt.Sprintf(" · %d 个素材失败 · %s", failed, issue)
	}
	return completed
}

func changed(before, after os.FileInfo) bool {
	return before == nil || after == nil || !os.SameFile(before, after) ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime())
}
