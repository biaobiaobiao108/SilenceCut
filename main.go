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
	"github.com/egoist/mygo/ui/video"

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
		s.Before, s.After = 250_000, 250_000
	case "中等":
		s.Before, s.After = 150_000, 150_000
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
	automatic   float64
	settings    timeline.Settings
	history     timeline.History
	filtered    []timeline.Clip
	analyzing   bool
	issue       string
	exportIssue string
	source      sourceStamp
	offline     bool
}

type player interface {
	Play()
	Pause()
	Playing() bool
	Position() time.Duration
	Seek(time.Duration)
	Close()
}

type app struct {
	win                            *mygo.Window
	workers                        sync.WaitGroup
	assets                         []*asset
	selected                       *asset
	opts                           filterOptions
	cancel                         context.CancelFunc
	previewSeq                     int
	job, status, outputDir         string
	publishedDir                   string
	progress                       float64
	picking, closing, canceling    bool
	settingsOpen, validateSettings bool
	refilter                       []*asset
	refilterCount                  int
	onlyKept, originalPreview      bool
	player                         player
	video                          *ui.VideoPlayer
	previewIssue                   string
	previewSince, videoSeekAt      time.Time
	wasPlaying                     bool
	position                       int64
	selection                      timeline.Span
	viewStart                      int64
	viewLength                     int64
	waveDrag                       waveGesture
	overviewDrag                   overviewGesture
	waveColumns                    []timeline.Peak
	overviewColumns                []timeline.Peak
	projectPath                    string
	projectDirty, closeApproved    bool
	recovery                       *recoveryFile
	recoveryAt                     time.Time
}

func main() {
	state := &app{
		opts: filterOptions{preset: "慢", level: "-40", before: "0.10", after: "0.10"},
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
			Content: ui.View(state.view),
		})
		if runtime.GOOS == "windows" {
			var icon bytes.Buffer
			if err := png.Encode(&icon, appIcon.Image(128, 128, nil)); err != nil {
				log.Print(err)
			} else if err := state.win.SetIcon(icon.Bytes()); err != nil {
				log.Print(err)
			}
		}
		state.win.OnClose(func(e *mygo.CloseEvent) {
			if state.picking {
				e.PreventDefault()
			} else if state.projectDirty && !state.closeApproved && state.cancel == nil {
				e.PreventDefault()
				state.guardProject(func() {
					state.closeApproved = true
					state.win.Close()
				})
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
	selected := a.selected
	if selected == nil || a.busy() {
		return
	}
	index := slices.Index(a.assets, selected)
	a.assets = slices.Delete(a.assets, index, index+1)
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
}

func (a *app) report() media.Progress {
	last := time.Time{}
	return func(value float64) {
		if value < 1 && time.Since(last) < 80*time.Millisecond {
			return
		}
		last = time.Now()
		a.win.Update(func() { a.progress = max(a.progress, value) })
	}
}

func (a *app) validSettings() bool {
	_, err := a.opts.settings(0)
	a.validateSettings = err != nil
	if a.validateSettings {
		a.settingsOpen = true
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
			var err error
			var clips []timeline.Clip
			stat, statErr := os.Stat(current.path)
			if statErr != nil {
				err = statErr
			} else if current.info.Duration == 0 || changed(current.file, stat) {
				current.info, current.peaks, err = media.Waveform(ctx, current.path, func(value float64) { update(value * .45) })
				if err == nil {
					current.waveform = timeline.BuildWaveform(current.peaks)
					current.automatic = timeline.AutomaticThreshold(current.peaks)
					current.file = stat
				}
			}
			if err == nil {
				current.settings, _ = opts.settings(current.automatic)
				clips, err = media.Detect(ctx, current.path, current.settings, func(value float64) { update(.45 + value*.55) })
			}
			if err != nil && ctx.Err() != nil {
				break
			}
			update(1)
			if err != nil {
				failed++
				a.win.Update(func() {
					item.analyzing = false
					item.issue = err.Error()
				})
				continue
			}
			succeeded++
			current.history = timeline.History{Clips: clips}
			a.win.Update(func() {
				reload := item.info.Duration == 0 || item.offline || changed(item.file, current.file)
				if reload {
					if item == a.selected {
						a.closePreview()
					}
				}
				item.info, item.peaks, item.automatic = current.info, current.peaks, current.automatic
				item.waveform = current.waveform
				item.file, item.settings, item.history = current.file, current.settings, current.history
				item.filtered, item.analyzing, item.issue, item.exportIssue = clips, false, "", ""
				item.offline = false
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
		status := batchStatus("过滤", fmt.Sprintf("已过滤 %d 个素材", succeeded), succeeded, failed, "详情见素材列表", ctx.Err() != nil)
		a.win.Update(func() {
			for _, item := range items {
				item.analyzing = false
			}
			a.end(status)
		})
	})
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

func (a *app) chooseExport(clips bool) {
	if a.busy() {
		return
	}
	count := 0
	for _, item := range a.assets {
		if a.exportable(item, clips) {
			count++
		}
	}
	if count == 0 {
		return
	}
	a.pick(mygo.OpenDialogOptions{
		Parent: a.win, Title: fmt.Sprintf("为 %d 个素材选择导出文件夹", count), DefaultPath: a.outputDir,
		Directory: true, CreateDirectories: true, ButtonLabel: fmt.Sprintf("导出 %d 个素材", count),
	}, func(paths []string) { a.export(paths[0], clips) })
}

func (a *app) canExport(clips bool) bool {
	return slices.ContainsFunc(a.assets, func(item *asset) bool { return a.exportable(item, clips) })
}

func (a *app) exportable(item *asset, clips bool) bool {
	return item.issue == "" && (clips && !a.onlyKept && len(item.history.Clips) > 0 ||
		slices.ContainsFunc(item.history.Clips, func(clip timeline.Clip) bool { return clip.Keep }))
}

func (a *app) export(dir string, clips bool) {
	a.outputDir = dir
	type exportWork struct {
		item     *asset
		snapshot asset
	}
	type exportFailure struct {
		item  *asset
		issue string
	}
	var work []exportWork
	for _, item := range a.assets {
		if a.exportable(item, clips) {
			item.exportIssue = ""
			work = append(work, exportWork{item: item, snapshot: *item})
		}
	}
	onlyKept := a.onlyKept
	ctx := a.begin("导出")
	a.async(func() {
		report := a.report()
		var count, failures int
		var failed []exportFailure
		for index, item := range work {
			if ctx.Err() != nil {
				break
			}
			var n int
			current, err := os.Stat(item.snapshot.path)
			if err == nil && changed(item.snapshot.file, current) {
				err = errors.New("素材已改变，请先重新过滤")
			}
			if err == nil {
				update := func(value float64) { report((float64(index) + value) / float64(len(work))) }
				n, err = exportAsset(ctx, item.snapshot, dir, clips, onlyKept, update)
			}
			count += n
			if err != nil && !errors.Is(err, context.Canceled) {
				failures++
				failed = append(failed, exportFailure{item: item.item, issue: err.Error()})
			}
		}
		detail := "详情见素材列表"
		if failures == 0 {
			detail = ""
		}
		status := batchStatus("导出", fmt.Sprintf("已导出 %d 个文件", count), count, failures, detail, ctx.Err() != nil)
		a.win.Update(func() {
			for _, item := range failed {
				item.item.exportIssue = item.issue
			}
			if count > 0 {
				a.publishedDir = dir
			}
			a.end(status)
		})
	})
}

func exportAsset(ctx context.Context, item asset, dir string, clips, onlyKept bool, progress media.Progress) (int, error) {
	base := strings.TrimSuffix(item.name, filepath.Ext(item.name))
	if !clips {
		spans := timeline.Kept(item.history.Clips)
		if len(spans) == 0 {
			return 0, nil
		}
		_, err := media.Export(ctx, item.path, dir, base+"_已过滤", spans, progress)
		if err != nil {
			return 0, err
		}
		return 1, nil
	}
	parts := item.history.Clips
	if onlyKept {
		parts = slices.DeleteFunc(slices.Clone(parts), func(clip timeline.Clip) bool { return !clip.Keep })
	}
	if len(parts) == 0 {
		return 0, nil
	}
	folder, err := clipFolder(dir, base+"_切片")
	if err != nil {
		return 0, err
	}
	count := 0
	for index, part := range parts {
		label := "移除"
		if part.Keep {
			label = "保留"
		}
		name := fmt.Sprintf("%03d_%s", index+1, label)
		_, err := media.Export(ctx, item.path, folder, name, []timeline.Span{part.Span}, func(value float64) {
			progress((float64(index) + value) / float64(len(parts)))
		})
		if err != nil {
			if count == 0 {
				_ = os.Remove(folder)
			}
			return count, err
		}
		count++
	}
	return count, nil
}

func clipFolder(dir, base string) (string, error) {
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s (%d)", base, n)
		}
		path := filepath.Join(dir, name)
		err := os.Mkdir(path, 0755)
		if !errors.Is(err, os.ErrExist) {
			return path, err
		}
	}
}

func (a *app) closePreview() {
	a.previewSeq++
	if a.player != nil {
		a.player.Close()
	}
	a.player, a.video, a.previewIssue = nil, nil, ""
	a.previewSince, a.videoSeekAt = time.Time{}, time.Time{}
	a.wasPlaying = false
}

func (a *app) openPreview(item *asset) {
	if item.offline {
		return
	}
	if item.info.Video {
		p, err := video.Open(item.path)
		if err != nil {
			a.previewIssue = err.Error()
			return
		}
		p.Pause()
		a.player, a.video, a.previewSince = p, p, time.Now()
		return
	}
	p, err := openAudioPreview(item.path, nil)
	if err != nil {
		a.previewIssue = err.Error()
		return
	}
	a.player = p
	a.seek(a.position)
}

func (a *app) seek(position int64) {
	if a.selected == nil {
		return
	}
	a.position = max(0, min(position, a.selected.info.Duration))
	a.scheduleRecovery()
	if a.player == nil || a.video != nil && !a.video.Ready() {
		return
	}
	if a.selected.info.Video {
		a.videoSeekAt = time.Now()
	}
	a.player.Seek(time.Duration(a.position) * time.Microsecond)
}

func (a *app) togglePlay() {
	if a.busy() || a.waveDrag.kind != waveIdle || a.selected == nil || a.selected.offline ||
		a.player == nil || a.video != nil && !a.video.Ready() {
		return
	}
	if a.player.Playing() {
		a.player.Pause()
		a.updatePlaybackPosition()
		return
	}
	clips := a.selected.history.Clips
	target := a.position
	if !a.originalPreview {
		var ok bool
		if target, ok = nextRetained(clips, target); !ok {
			if target, ok = nextRetained(clips, 0); !ok {
				return
			}
		}
	} else if target >= a.selected.info.Duration {
		target = 0
	}
	if !a.selected.info.Video && !a.prepareAudioPlayback(clips) {
		return
	}
	if a.video != nil && a.video.Ended() ||
		target != a.player.Position().Microseconds() && (target != a.position || a.videoSeekAt.IsZero()) {
		a.seek(target)
	}
	a.position = target
	a.followPlayhead()
	a.player.Play()
	a.wasPlaying = a.player.Playing()
}

func (a *app) prepareAudioPlayback(clips []timeline.Clip) bool {
	cut, edited := a.player.(*auditionPlayer)
	if a.originalPreview && !edited || !a.originalPreview && edited && slices.Equal(cut.clips, clips) {
		return true
	}
	if a.originalPreview {
		clips = nil
	}
	p, err := openAudioPreview(a.selected.path, clips)
	if err != nil {
		a.status = "无法开始试听 · " + err.Error()
		return false
	}
	a.player.Close()
	a.player = p
	return true
}

func (a *app) setPreviewMode(original bool) {
	playing := a.player != nil && a.player.Playing()
	if playing {
		a.player.Pause()
		a.updatePlaybackPosition()
	}
	a.originalPreview = original
	a.markProject()
	if playing {
		if _, ok := nextRetained(a.selected.history.Clips, a.position); original || ok {
			a.togglePlay()
		}
	}
}

func (a *app) previewPlayable(kept int64) bool {
	return a.selected != nil && !a.selected.offline && !a.busy() && a.player != nil &&
		(a.video == nil || a.video.Ready()) && (a.originalPreview || kept > 0)
}

func nextRetained(clips []timeline.Clip, at int64) (int64, bool) {
	i := clipAt(clips, at)
	if i < len(clips) && !clips[i].Keep {
		i++
	}
	if i < len(clips) && clips[i].Keep {
		return max(at, clips[i].Start), true
	}
	return 0, false
}

func (a *app) updatePlaybackPosition() bool {
	if a.player == nil {
		return false
	}
	playing := a.player.Playing()
	if !playing && !a.wasPlaying {
		return false
	}
	a.wasPlaying = playing
	position := max(0, min(a.player.Position().Microseconds(), a.selected.info.Duration))
	if !a.videoSeekAt.IsZero() {
		// Native seeks can report the previous timestamp until the decoder lands.
		elapsed := time.Since(a.videoSeekAt)
		delta := position - a.position
		if delta < -1_000 || delta > elapsed.Microseconds()+100_000 {
			if elapsed < 2*time.Second {
				return playing
			}
			a.stopPlayback()
			a.videoSeekAt = time.Time{}
			a.status = "视频定位超时，请重新定位后播放"
			return false
		}
		a.videoSeekAt = time.Time{}
	}
	if a.selected.info.Video {
		position = max(a.position, position)
	}
	if a.selected.info.Video && !a.originalPreview {
		clips := a.selected.history.Clips
		if len(clips) > 0 {
			last := clips[len(clips)-1]
			if !last.Keep && position >= last.Start {
				a.stopPlayback()
				a.seek(last.Start)
				position, playing = last.Start, false
			} else if playing {
				if target, ok := nextRetained(clips, position); ok && target > position {
					a.seek(target)
					position = target
				}
			}
		}
	}
	a.position = position
	if !a.overviewDrag.active {
		a.followPlayhead()
	}
	a.scheduleRecovery()
	return playing
}
