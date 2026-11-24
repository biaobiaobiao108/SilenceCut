package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

const projectExt = ".silencecut"

var projectTypes = []mygo.FileFilter{{Name: "SilenceCut 项目", Extensions: []string{"silencecut"}}}

type sourceStamp struct {
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

func stampOf(file os.FileInfo) sourceStamp {
	if file == nil {
		return sourceStamp{}
	}
	return sourceStamp{file.Size(), file.ModTime().UTC()}
}

type projectAsset struct {
	Path      string            `json:"path"`
	Source    sourceStamp       `json:"source"`
	Info      media.Info        `json:"media"`
	Automatic float64           `json:"automatic_threshold"`
	Settings  timeline.Settings `json:"settings"`
	Filtered  []timeline.Clip   `json:"filtered"`
	Clips     []timeline.Clip   `json:"clips"`
}

type projectDocument struct {
	Version         int            `json:"version"`
	Project         string         `json:"recover_project,omitempty"`
	Assets          []projectAsset `json:"assets"`
	Preset          string         `json:"preset"`
	Level           string         `json:"level"`
	Before          string         `json:"before"`
	After           string         `json:"after"`
	Ends            bool           `json:"only_ends"`
	OnlyKept        bool           `json:"only_kept"`
	OriginalPreview bool           `json:"original_preview"`
	Selected        int            `json:"selected"`
	Position        int64          `json:"position"`
	ViewStart       int64          `json:"view_start"`
	ViewLength      int64          `json:"view_length"`
	Selection       timeline.Span  `json:"selection"`
}

func (a *app) snapshot(path string) projectDocument {
	doc := projectDocument{
		Version: 1, Preset: a.opts.preset, Level: a.opts.level, Before: a.opts.before, After: a.opts.after,
		Ends: a.opts.ends, OnlyKept: a.onlyKept, OriginalPreview: a.originalPreview,
		Selected: slices.Index(a.assets, a.selected), Position: a.position, ViewStart: a.viewStart, ViewLength: a.viewLength,
		Selection: a.selection,
	}
	for _, item := range a.assets {
		source := item.path
		if path != "" {
			if relative, err := filepath.Rel(filepath.Dir(path), source); err == nil {
				source = relative
			}
		}
		identity := item.source
		if item.file != nil {
			identity = stampOf(item.file)
		}
		doc.Assets = append(doc.Assets, projectAsset{
			Path: filepath.ToSlash(source), Source: identity, Info: item.info, Automatic: item.automatic,
			Settings: item.settings, Filtered: item.filtered, Clips: item.history.Clips,
		})
	}
	return doc
}

func writeProject(path string, doc projectDocument) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".silencecut-project-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(doc)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceProject(f.Name(), path)
}

func readProject(path string) (projectDocument, error) {
	var doc projectDocument
	f, err := os.Open(path)
	if err != nil {
		return doc, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	if err := decoder.Decode(&doc); err != nil {
		return doc, err
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return doc, errors.New("项目文件含有多余内容")
	}
	if doc.Version != 1 {
		return doc, fmt.Errorf("不支持项目文件版本 %d", doc.Version)
	}
	for _, item := range doc.Assets {
		if item.Path == "" || item.Info.Duration < 0 || item.Info.Duration > int64((1<<63-1)/time.Microsecond) ||
			item.Info.Duration > 0 && (item.Info.Rate <= 0 || item.Info.Channels <= 0) ||
			!validProjectClips(item.Clips, item.Info.Duration) || !validProjectClips(item.Filtered, item.Info.Duration) {
			return doc, fmt.Errorf("素材记录无效 · %s", item.Path)
		}
	}
	return doc, nil
}

func validProjectClips(clips []timeline.Clip, duration int64) bool {
	var end int64
	for i, clip := range clips {
		if clip.Start != end || clip.End <= end || clip.End > duration || i > 0 && clips[i-1].Keep == clip.Keep {
			return false
		}
		end = clip.End
	}
	return end == duration
}

func (a *app) markProject() {
	if a.projectPath != "" || len(a.assets) > 0 || a.projectDirty {
		a.projectDirty = true
		a.scheduleRecovery()
		a.scheduleChunkWarm()
	}
}

func (a *app) scheduleRecovery() {
	if a.projectDirty && a.recovery != nil && a.recoveryAt.IsZero() {
		a.recoveryAt = time.Now().Add(time.Second)
	}
}

func (a *app) recoveryTick(c *ui.Context) {
	if a.recoveryAt.IsZero() {
		return
	}
	if delay := time.Until(a.recoveryAt); delay > 0 {
		c.After(delay)
		return
	}
	a.recoveryAt = time.Time{}
	if err := a.writeRecovery(); err != nil {
		a.status = "无法写入恢复快照 · " + err.Error()
	}
}

func (a *app) writeRecovery() error {
	if a.recovery == nil || !a.projectDirty {
		return nil
	}
	doc := a.snapshot("")
	doc.Project = a.projectPath
	return writeProject(a.recovery.file.Name()+projectExt, doc)
}

func (a *app) clearRecovery() {
	a.recoveryAt = time.Time{}
	if a.recovery != nil {
		removeRecovery(a.recovery.file.Name() + projectExt)
	}
}

func removeRecovery(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Print(err)
	}
}

type recoveryFile struct{ file *os.File }

func openRecovery() (*recoveryFile, *recoveryFile, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return nil, nil, err
	}
	return openRecoveryAt(filepath.Join(config, "SilenceCut", "Recovery"))
}

func openRecoveryAt(dir string) (*recoveryFile, *recoveryFile, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "session-*"+projectExt))
	if err != nil {
		return nil, nil, err
	}
	var previous *recoveryFile
	var newest time.Time
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil || !info.ModTime().After(newest) {
			continue
		}
		f, err := os.OpenFile(strings.TrimSuffix(path, projectExt), os.O_RDWR, 0600)
		if err != nil {
			continue
		}
		if err := lockRecovery(f); err != nil {
			f.Close()
			continue
		}
		if previous != nil {
			previous.file.Close()
		}
		previous, newest = &recoveryFile{f}, info.ModTime()
	}
	f, err := os.CreateTemp(dir, "session-*")
	if err == nil {
		err = lockRecovery(f)
	}
	if err != nil {
		if f != nil {
			f.Close()
			removeRecovery(f.Name())
		}
		return nil, previous, err
	}
	return &recoveryFile{f}, previous, nil
}

func (r *recoveryFile) close(discard bool) {
	if r == nil || r.file == nil {
		return
	}
	path := r.file.Name()
	if discard {
		removeRecovery(path + projectExt)
	}
	r.file.Close()
	r.file = nil
	if _, err := os.Stat(path + projectExt); errors.Is(err, os.ErrNotExist) {
		removeRecovery(path)
	}
}

func (a *app) offerRecovery(previous *recoveryFile) {
	doc, err := readProject(previous.file.Name() + projectExt)
	if err != nil {
		a.status = "无法读取恢复快照 · " + err.Error()
		previous.close(false)
		return
	}
	a.projectPrompt("恢复上次编辑？", "发现上次退出时留下的未保存项目。", []string{"恢复", "丢弃", "稍后"}, func(choice int) {
		discard := choice == 1
		if choice == 0 {
			a.loadProject(doc, doc.Project, true)
			discard = a.recovery != nil && a.writeRecovery() == nil
		}
		previous.close(discard)
	})
}

func (a *app) projectPrompt(message, detail string, buttons []string, done func(int)) {
	a.picking = true
	a.async(func() {
		result, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: a.win, Type: mygo.MessageQuestion, Title: title, Message: message, Detail: detail,
			Buttons: buttons, DefaultButton: 0, CancelButton: len(buttons) - 1,
		})
		a.win.Update(func() {
			a.picking = false
			if a.closing {
				return
			}
			if err != nil {
				a.status = err.Error()
				done(-1)
				return
			}
			done(result.Button)
		})
	})
}

func (a *app) guardProject(done func()) {
	if !a.projectDirty {
		done()
		return
	}
	a.projectPrompt("保存当前项目？", "当前项目有未保存的改动。", []string{"保存", "不保存", "取消"}, func(choice int) {
		switch choice {
		case 0:
			a.chooseSaveProject(done)
		case 1:
			done()
		}
	})
}

func (a *app) chooseSaveProject(done func()) {
	if a.busy() || a.projectPath == "" && len(a.assets) == 0 && !a.projectDirty {
		return
	}
	save := func(path string) {
		path, err := filepath.Abs(path)
		if err != nil {
			a.status = "无法保存项目 · " + err.Error()
			return
		}
		if err := writeProject(path, a.snapshot(path)); err != nil {
			a.status = "无法保存项目 · " + err.Error()
			return
		}
		a.projectPath, a.projectDirty = path, false
		a.clearRecovery()
		a.refreshProjectTitle()
		a.status = "已保存项目 · " + filepath.Base(path)
		if done != nil {
			done()
		}
	}
	if a.projectPath != "" {
		save(a.projectPath)
		return
	}
	a.picking = true
	a.async(func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent: a.win, Title: "保存项目", DefaultPath: "未命名" + projectExt, Filters: projectTypes,
		})
		a.win.Update(func() {
			a.picking = false
			if a.closing {
				return
			}
			if err != nil {
				a.status = err.Error()
			} else if path != "" {
				if !isProject(path) {
					path += projectExt
					if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
						a.status = "文件已存在 · " + filepath.Base(path)
						return
					}
				}
				save(path)
			}
		})
	})
}

func isProject(path string) bool { return strings.EqualFold(filepath.Ext(path), projectExt) }

func (a *app) chooseOpenProject() {
	if a.busy() {
		return
	}
	a.pick(mygo.OpenDialogOptions{Parent: a.win, Title: "打开项目", Filters: projectTypes}, func(paths []string) {
		a.openProject(paths[0])
	})
}

func (a *app) openProject(path string) {
	if a.busy() {
		return
	}
	path, err := filepath.Abs(path)
	if err != nil {
		a.status = "无法打开项目 · " + err.Error()
		return
	}
	doc, err := readProject(path)
	if err != nil {
		a.status = "无法打开项目 · " + err.Error()
		return
	}
	a.guardProject(func() { a.loadProject(doc, path, false) })
}

func (a *app) refreshProjectTitle() {
	if a.win == nil {
		return
	}
	if a.projectPath != "" {
		a.win.SetTitle(title + " · " + filepath.Base(a.projectPath))
	} else {
		a.win.SetTitle(title)
	}
}

func (a *app) loadProject(doc projectDocument, path string, recovered bool) {
	a.closePreview()
	for _, item := range a.assets {
		clearAssetPreviews(item)
	}
	a.clearRecovery()
	a.projectPath, a.projectDirty = path, recovered
	a.refreshProjectTitle()
	a.assets, a.selected = nil, nil
	a.opts = filterOptions{preset: doc.Preset, level: doc.Level, before: doc.Before, after: doc.After, ends: doc.Ends}
	a.onlyKept, a.originalPreview = doc.OnlyKept, doc.OriginalPreview
	a.selection, a.waveDrag, a.overviewDrag = timeline.Span{}, waveGesture{}, overviewGesture{}
	for _, saved := range doc.Assets {
		source := filepath.FromSlash(saved.Path)
		if !filepath.IsAbs(source) {
			source = filepath.Join(filepath.Dir(path), source)
		}
		item := &asset{
			path: source, name: filepath.Base(source), source: saved.Source, info: saved.Info,
			automatic: saved.Automatic, settings: saved.Settings, filtered: saved.Filtered,
			history: timeline.History{Clips: saved.Clips}, offline: true, issue: "待检查素材",
		}
		a.assets = append(a.assets, item)
	}
	if len(a.assets) > 0 {
		a.selected = a.assets[max(0, min(len(a.assets)-1, doc.Selected))]
	}
	a.position, a.viewStart, a.viewLength = 0, 0, 0
	if a.selected != nil {
		duration := a.selected.info.Duration
		a.position = min(duration, max(0, doc.Position))
		a.viewLength = min(duration, max(0, doc.ViewLength))
		a.viewStart = min(max(0, duration-a.detailLength(duration)), max(0, doc.ViewStart))
		if doc.Selection.Start >= 0 && doc.Selection.End > doc.Selection.Start && doc.Selection.End <= duration {
			a.selection = doc.Selection
		}
	}
	if len(a.assets) > 0 {
		a.loadProjectSources(a.assets, "")
	} else {
		a.status = "已打开项目"
	}
}

// A relink accepts a new source explicitly. Its clock must match the saved
// cuts. A separate refilter action can replace cuts for a changed duration.
func (a *app) loadProjectSources(items []*asset, relink string) {
	ctx := a.begin("读取项目素材")
	a.async(func() {
		report := a.report()
		var bad int
		for i, item := range items {
			if ctx.Err() != nil {
				break
			}
			source := item.path
			if relink != "" {
				source = relink
			}
			file, err := os.Stat(source)
			if err == nil && relink == "" && item.source != (sourceStamp{}) && stampOf(file) != item.source {
				err = errors.New("源文件已改变，请重新定位")
			}
			var info media.Info
			var peaks []timeline.Peak
			if err == nil {
				info, peaks, err = media.Waveform(ctx, source, func(f float64) {
					report((float64(i) + f) / float64(len(items)))
				})
			}
			if err == nil && item.info.Duration != 0 &&
				(info.Duration != item.info.Duration || info.Video != item.info.Video) {
				err = errors.New("素材类型或时长与项目不同")
			}
			if ctx.Err() != nil {
				break
			}
			if err != nil {
				bad++
			}
			wave := timeline.BuildWaveform(peaks)
			a.win.Update(func() {
				if err != nil {
					item.issue, item.offline = err.Error(), true
					return
				}
				item.path, item.name, item.file, item.source = source, filepath.Base(source), file, stampOf(file)
				item.info, item.peaks, item.waveform = info, peaks, wave
				item.issue, item.offline = "", false
				if len(item.history.Clips) == 0 {
					// A crash can interrupt the first analysis. The user can retry it.
					item.info = media.Info{}
					item.issue = "首次过滤未完成，请重新过滤"
				}
				if relink != "" {
					a.markProject()
				}
			})
		}
		a.win.Update(func() {
			if a.selected != nil && a.selected.file != nil && !a.selected.offline && a.selected.info.Duration > 0 {
				a.openPreview(a.selected, false)
				a.primeFilteredVideo()
			}
			status := "已读取项目素材"
			if ctx.Err() != nil {
				status = "已取消读取项目素材"
			} else if bad > 0 {
				status = fmt.Sprintf("%d 个素材需要重新定位", bad)
			}
			a.end(status)
		})
	})
}

func (a *app) relinkProjectSource(item *asset) {
	if a.busy() {
		return
	}
	a.pick(mygo.OpenDialogOptions{Parent: a.win, Title: "重新定位素材", DefaultPath: item.path,
		Filters: []mygo.FileFilter{{Name: "音视频", Extensions: fileTypes}}}, func(paths []string) {
		path := paths[0]
		file, err := os.Stat(path)
		if err != nil {
			a.status = err.Error()
			return
		}
		relink := func() { a.loadProjectSources([]*asset{item}, path) }
		if stampOf(file) == item.source {
			relink()
			return
		}
		a.projectPrompt("使用这个素材？", "文件与原素材的记录不同。若时长一致，项目将保留现有切点。", []string{"使用并保留切点", "取消"}, func(choice int) {
			if choice == 0 {
				relink()
			}
		})
	})
}
