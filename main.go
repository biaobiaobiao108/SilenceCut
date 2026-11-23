package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	_ "github.com/egoist/mygo/audio/aac"
	_ "github.com/egoist/mygo/audio/flac"
	_ "github.com/egoist/mygo/audio/mp3"
	_ "github.com/egoist/mygo/audio/mp4"
	_ "github.com/egoist/mygo/audio/vorbis"
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
		level, le := strconv.ParseFloat(f.level, 64)
		before, be := strconv.ParseFloat(f.before, 64)
		after, ae := strconv.ParseFloat(f.after, 64)
		if le != nil || math.IsNaN(level) || level < -70 || level > 20 {
			return s, errors.New("阈值请输入 −70 到 20 dB")
		}
		if be != nil || ae != nil || math.IsNaN(before) || math.IsNaN(after) || min(before, after) < 0 || max(before, after) > 10 {
			return s, errors.New("前后留白请输入 0 到 10 秒")
		}
		s.Threshold = level
		s.Before, s.After = int64(math.Round(before*1e6)), int64(math.Round(after*1e6))
	}
	return s, nil
}

type asset struct {
	path, name string
	file       os.FileInfo
	info       media.Info
	peaks      []timeline.Peak
	waveform   timeline.Waveform
	automatic  float64
	settings   timeline.Settings
	history    timeline.History
	issue      string
	proxy      string
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
	win                       *mygo.Window
	temp                      string
	workers                   sync.WaitGroup
	assets                    []*asset
	selected                  *asset
	opts                      filterOptions
	cancel                    context.CancelFunc
	previewCancel             context.CancelFunc
	previewSeq                int
	job, status, outputDir    string
	progress                  float64
	picking, closing          bool
	onlyKept, originalPreview bool
	player                    player
	video                     *ui.VideoPlayer
	previewIssue              string
	previewSince              time.Time
	wasPlaying                bool
	position                  int64
	selection                 timeline.Span
	viewStart                 int64
	viewLength                int64
	waveDrag                  waveGesture
	overviewDrag              overviewGesture
	waveColumns               []timeline.Peak
	overviewColumns           []timeline.Peak
}

func main() {
	temp, err := os.MkdirTemp("", "silencecut-*")
	if err != nil {
		log.Fatal(err)
	}
	state := &app{
		temp: temp,
		opts: filterOptions{preset: "慢", level: "-40", before: "0.10", after: "0.10"},
	}
	mygo.App.WhenReady(func() {
		state.win = mygo.NewWindow(mygo.WindowOptions{
			Title: title, Width: 1180, Height: 790, MinWidth: 930, MinHeight: 690,
			Content: ui.View(state.view),
		})
		state.win.OnClose(func(*mygo.CloseEvent) {
			state.closing = true
			state.closePreview()
			if state.cancel != nil {
				state.cancel()
			}
		})
		if len(os.Args) > 1 {
			state.add(os.Args[1:])
		}
	})
	err = mygo.App.Run()
	state.workers.Wait()
	if removeErr := os.RemoveAll(temp); removeErr != nil {
		log.Print(removeErr)
	}
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

func (a *app) busy() bool { return a.cancel != nil || a.picking }

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
	if _, err := a.opts.settings(0); err != nil {
		a.status = err.Error()
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
	if item != nil && item.info.Duration > 0 {
		a.openPreview(item, false)
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
	a.status = ""
}

func (a *app) begin(label string) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel, a.job, a.status, a.progress = cancel, label, "", 0
	return ctx
}

func (a *app) end(status string) {
	a.cancel()
	a.cancel, a.job, a.status = nil, "", status
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

func (a *app) filter(items []*asset) {
	if a.busy() || len(items) == 0 {
		return
	}
	opts := a.opts
	if _, err := opts.settings(0); err != nil {
		a.status = err.Error()
		return
	}
	ctx := a.begin("正在过滤")
	if a.player != nil {
		a.player.Pause()
	}
	work := make([]asset, 0, len(items))
	for _, item := range items {
		work = append(work, *item)
		item.issue = ""
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
					current.automatic = timeline.AutomaticThreshold(current.peaks, current.info.Rate)
					current.file, current.proxy = stat, ""
				}
			}
			if err == nil {
				current.settings, _ = opts.settings(current.automatic)
				clips, err = media.Detect(ctx, current.path, current.settings, func(value float64) { update(.45 + value*.55) })
			}
			if ctx.Err() != nil {
				break
			}
			update(1)
			if err != nil {
				failed++
				a.win.Update(func() { item.issue = err.Error() })
				continue
			}
			succeeded++
			current.history = timeline.History{Clips: clips}
			a.win.Update(func() {
				reload := item.info.Duration == 0 || changed(item.file, current.file)
				item.info, item.peaks, item.automatic = current.info, current.peaks, current.automatic
				item.waveform = current.waveform
				item.file, item.settings, item.history = current.file, current.settings, current.history
				if reload {
					item.proxy = ""
				}
				if item == a.selected {
					a.selection = timeline.Span{}
					if reload {
						a.closePreview()
						a.position, a.viewStart, a.viewLength = 0, 0, 0
						a.openPreview(item, false)
					}
				}
			})
		}
		status := fmt.Sprintf("已过滤 %d 个素材", succeeded)
		if failed > 0 {
			status += fmt.Sprintf(" · %d 个失败，详情见素材列表", failed)
		}
		if ctx.Err() != nil {
			status = "已取消过滤 · " + status
		}
		a.win.Update(func() { a.end(status) })
	})
}

func changed(before, after os.FileInfo) bool {
	return before == nil || after == nil || !os.SameFile(before, after) ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime())
}

func (a *app) chooseExport(clips bool) {
	if a.busy() || !a.canExport(clips) {
		return
	}
	a.pick(mygo.OpenDialogOptions{
		Parent: a.win, Title: "选择导出文件夹", DefaultPath: a.outputDir,
		Directory: true, CreateDirectories: true, ButtonLabel: "导出",
	}, func(paths []string) { a.export(paths[0], clips) })
}

func (a *app) canExport(clips bool) bool {
	return slices.ContainsFunc(a.assets, func(item *asset) bool {
		return item.issue == "" && (clips && !a.onlyKept && len(item.history.Clips) > 0 || len(timeline.Kept(item.history.Clips)) > 0)
	})
}

func (a *app) export(dir string, clips bool) {
	a.outputDir = dir
	var work []asset
	for _, item := range a.assets {
		if item.issue == "" && len(item.history.Clips) > 0 {
			work = append(work, *item)
		}
	}
	onlyKept := a.onlyKept
	ctx := a.begin("正在导出")
	if a.player != nil {
		a.player.Pause()
	}
	a.async(func() {
		report := a.report()
		var count, failures int
		var firstIssue string
		for index, item := range work {
			if ctx.Err() != nil {
				break
			}
			var n int
			current, err := os.Stat(item.path)
			if err == nil && changed(item.file, current) {
				err = errors.New("素材已改变，请先重新过滤")
			}
			if err == nil {
				update := func(value float64) { report((float64(index) + value) / float64(len(work))) }
				n, err = exportAsset(ctx, item, dir, clips, onlyKept, update)
			}
			count += n
			if err != nil && !errors.Is(err, context.Canceled) {
				failures++
				if firstIssue == "" {
					firstIssue = item.name + " · " + err.Error()
				}
			}
		}
		status := fmt.Sprintf("已导出 %d 个文件", count)
		if failures > 0 {
			status += fmt.Sprintf(" · %d 个素材失败 · %s", failures, firstIssue)
		}
		if ctx.Err() != nil {
			status = "已取消导出 · " + status
		}
		a.win.Update(func() { a.end(status) })
	})
}

func exportAsset(ctx context.Context, item asset, dir string, clips, onlyKept bool, progress media.Progress) (int, error) {
	base := strings.TrimSuffix(item.name, filepath.Ext(item.name))
	if !clips {
		spans := timeline.Kept(item.history.Clips)
		if len(spans) == 0 {
			return 0, nil
		}
		_, err := media.Export(ctx, item.path, dir, base+"_已过滤", spans, false, progress)
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
		_, err := media.Export(ctx, item.path, folder, name, []timeline.Span{part.Span}, false, func(value float64) {
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
	if a.previewCancel != nil {
		a.previewCancel()
		a.previewCancel = nil
	}
	if a.player != nil {
		a.player.Close()
	}
	a.player, a.video, a.previewIssue = nil, nil, ""
	a.wasPlaying = false
}

func (a *app) openPreview(item *asset, proxy bool) {
	path := item.path
	if item.proxy != "" {
		path, proxy = item.proxy, true
	}
	if item.info.Video {
		p, err := video.Open(path)
		if err == nil {
			p.Pause()
			a.player, a.video, a.previewSince = p, p, time.Now()
			p.Seek(time.Duration(a.position) * time.Microsecond)
			return
		}
		a.previewIssue = err.Error()
	} else {
		p, err := openAudioPreview(path, nil)
		if err == nil {
			a.player = p
			p.Seek(time.Duration(a.position) * time.Microsecond)
			return
		}
		a.previewIssue = err.Error()
	}
	if !proxy {
		a.makePreview(item)
	}
}

func (a *app) makePreview(item *asset) {
	a.closePreview()
	ctx, cancel := context.WithCancel(context.Background())
	a.previewCancel = cancel
	seq, dir := a.previewSeq, a.temp
	source, span := item.path, timeline.Span{End: item.info.Duration}
	a.async(func() {
		path, err := media.Export(ctx, source, dir, "preview", []timeline.Span{span}, true, func(float64) {})
		a.win.Update(func() {
			if a.closing || seq != a.previewSeq {
				if path != "" {
					_ = os.Remove(path)
				}
				return
			}
			cancel()
			a.previewCancel = nil
			if err != nil {
				a.previewIssue = err.Error()
				return
			}
			item.proxy = path
			a.openPreview(item, true)
		})
	})
}

func (a *app) seek(position int64) {
	if a.selected == nil {
		return
	}
	a.position = max(0, min(position, a.selected.info.Duration))
	if a.player != nil {
		a.player.Seek(time.Duration(a.position) * time.Microsecond)
	}
}

func (a *app) togglePlay() {
	if a.player == nil || a.busy() || a.video != nil && !a.video.Ready() {
		return
	}
	if a.player.Playing() {
		a.player.Pause()
		return
	}
	clips := a.selected.history.Clips
	if !a.originalPreview {
		first, ok := nextRetained(clips, 0)
		if !ok {
			return
		}
		if target, ok := nextRetained(clips, a.position); ok {
			a.position = target
		} else {
			a.position = first
		}
	} else if a.position >= a.selected.info.Duration {
		a.position = 0
	}
	if a.video == nil && !a.prepareAudioPlayback(clips) {
		return
	}
	a.seek(a.position)
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
	path := a.selected.path
	if a.selected.proxy != "" {
		path = a.selected.proxy
	}
	p, err := openAudioPreview(path, clips)
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
	a.stopPlayback()
	a.originalPreview = original
	if playing {
		if _, ok := nextRetained(a.selected.history.Clips, a.position); original || ok {
			a.togglePlay()
		}
	}
}

func (a *app) previewPlayable(kept int64) bool {
	return !a.busy() && a.player != nil && (a.video == nil || a.video.Ready()) &&
		(a.originalPreview || kept > 0)
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
	a.position = max(0, min(a.player.Position().Microseconds(), a.selected.info.Duration))
	if playing && !a.originalPreview && a.video != nil {
		if target, ok := nextRetained(a.selected.history.Clips, a.position); ok {
			if target != a.position {
				a.seek(target)
			}
		} else {
			a.player.Pause()
			clips := a.selected.history.Clips
			last := clipAt(clips, a.position) - 1
			if last >= 0 && clips[last].Keep {
				a.seek(clips[last].End)
			}
			playing = false
		}
	}
	if !a.overviewDrag.active {
		a.followPlayhead()
	}
	a.wasPlaying = playing
	return playing
}
