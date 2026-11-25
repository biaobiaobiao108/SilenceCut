package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

func TestLongWaveformNavigation(t *testing.T) {
	duration := int64(317) * timeline.Second
	item := &asset{
		name: "五分钟口播.m4a",
		info: media.Info{Duration: duration, Rate: 48000, Channels: 1},
	}
	for start := int64(0); start < duration; start += 6 * timeline.Second {
		end := min(start+5*timeline.Second, duration)
		item.history.Clips = append(item.history.Clips, timeline.Clip{Span: timeline.Span{Start: start, End: end}, Keep: true})
		if end < min(start+6*timeline.Second, duration) {
			item.history.Clips = append(item.history.Clips, timeline.Clip{Span: timeline.Span{Start: end, End: end + timeline.Second}})
		}
	}
	for i := range int(duration * 48000 / timeline.Second / timeline.PeakSamples) {
		peak := int16(500 * math.Abs(math.Sin(float64(i)/17)) * math.Abs(math.Sin(float64(i)/63)))
		if i%1125 >= 937 {
			peak = 0
		}
		item.peaks = append(item.peaks, timeline.Peak{Min: -peak, Max: peak})
	}
	item.waveform = timeline.BuildWaveform(item.peaks)
	if item.waveform.Amplitude < 250 || item.waveform.Amplitude > 550 {
		t.Fatalf("quiet material scale = %g", item.waveform.Amplitude)
	}
	a := &app{assets: []*asset{item}, selected: item, opts: filterOptions{preset: "慢"}}
	view := ui.NewTester(a.view, 1180, 790)
	wave, _ := view.Find("音频波形和播放位置")
	overview, _ := view.Find("全局波形")
	if wave.W == 0 || overview.W == 0 || a.detailLength(duration) != 30*timeline.Second {
		t.Fatalf("initial timeline = %v %v, %d µs", wave, overview, a.detailLength(duration))
	}
	a.viewStart = 5 * timeline.Second
	view.Frame()
	view.Wheel(wave.X+wave.W/2, wave.Y+wave.H/2, 90, 0)
	if a.viewStart <= 5*timeline.Second {
		t.Fatalf("horizontal wheel did not move the view forward: %d µs", a.viewStart)
	}
	forward := a.viewStart
	view.Wheel(wave.X+wave.W/2, wave.Y+wave.H/2, -45, 0)
	if a.viewStart >= forward {
		t.Fatalf("horizontal wheel did not move the view back: %d µs", a.viewStart)
	}
	a.viewStart = 0
	near := func(got, want, tolerance int64) {
		t.Helper()
		if got < want-tolerance || got > want+tolerance {
			t.Fatalf("got %d µs, want %d ± %d", got, want, tolerance)
		}
	}
	view.ClickAt(wave.X+wave.W/2, wave.Y+wave.H/2)
	near(a.position, 15*timeline.Second, 40_000)
	if err := view.Click("放大波形"); err != nil {
		t.Fatal(err)
	}
	near(a.detailLength(duration), 20*timeline.Second, 0)
	near(a.viewStart, 5*timeline.Second, 40_000)
	zoom, ok := view.Find("波形缩放")
	if !ok {
		t.Fatal("zoom slider is missing")
	}
	view.ClickAt(zoom.X+zoom.W*.25, zoom.Y+zoom.H/2)
	if a.detailLength(duration) <= 20*timeline.Second {
		t.Fatalf("zoom slider did not zoom out: %d µs", a.detailLength(duration))
	}
	view.ClickAt(overview.X+overview.W*.5, overview.Y+overview.H/2)
	near(a.position, duration/2, 500_000)
	near(a.viewStart, a.position-a.detailLength(duration)/2, 500_000)
	priorView, priorPosition := a.viewStart, a.position
	view.Press(overview.X+overview.W*.5, overview.Y+overview.H/2)
	view.Move(overview.X+overview.W*.58, overview.Y+overview.H/2)
	view.Release(overview.X+overview.W*.58, overview.Y+overview.H/2)
	near(a.viewStart, priorView+int64(float64(duration)*.08), 500_000)
	near(a.position, priorPosition, 0)

	a.viewStart, a.viewLength = 0, 30*timeline.Second
	view.Frame()
	x := func(second float32) float32 { return wave.X + wave.W*second/30 }
	view.Press(x(5), wave.Y+rulerHeight+4)
	view.Move(x(5.5), wave.Y+rulerHeight+4)
	view.Release(x(5.5), wave.Y+rulerHeight+4)
	near(item.history.Clips[0].End, 5_500_000, 40_000)
	if !item.history.CanUndo() {
		t.Fatal("moving a boundary must be undoable")
	}
	if err := view.Click("撤销"); err != nil {
		t.Fatal(err)
	}
	near(item.history.Clips[0].End, 5_000_000, 0)
	view.Press(x(1), wave.Y+wave.H/2)
	view.Move(x(2), wave.Y+wave.H/2)
	view.Release(x(2), wave.Y+wave.H/2)
	near(a.selection.Start, timeline.Second, 40_000)
	near(a.selection.End, 2*timeline.Second, 40_000)
	if err := view.Click("移除"); err != nil || item.history.Clips[1].Keep {
		t.Fatalf("range editing failed: %v, %+v", err, item.history.Clips[:3])
	}
	near(item.history.Clips[1].Start, timeline.Second, 40_000)
	near(item.history.Clips[1].End, 2*timeline.Second, 40_000)
}

func TestCaptureVoiceWaveform(t *testing.T) {
	source := os.Getenv("SILENCECUT_TEST_VOICE")
	folder := os.Getenv("SILENCECUT_TEST_CAPTURE")
	if source == "" || folder == "" {
		t.Skip("optional real-media capture")
	}
	info, peaks, err := media.Waveform(context.Background(), source, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	level := timeline.AutomaticThreshold(peaks)
	settings := timeline.Settings{Threshold: level, Before: 250_000, After: 250_000}
	clips, err := media.Detect(context.Background(), source, settings, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	item := &asset{
		name: filepath.Base(source), info: info, peaks: peaks, waveform: timeline.BuildWaveform(peaks),
		history: timeline.History{Clips: clips}, automatic: level, settings: settings,
	}
	p, err := openAudioPreview(source, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	a := &app{assets: []*asset{item}, selected: item, player: p, position: 17 * timeline.Second, opts: filterOptions{preset: "慢"}}
	view := ui.NewTester(a.view, 1180, 790)
	if len(clips) < 3 || info.Duration < 300*timeline.Second {
		t.Fatalf("expected a long recording with detected pauses, got %+v in %d µs at %.1f dB", clips, info.Duration, level)
	}
	f, err := os.Create(filepath.Join(folder, "five-minute-waveform.png"))
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, view.Image())
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("capture: %v %v", err, closeErr)
	}
}

func TestPendingWaveformIsReadOnly(t *testing.T) {
	for _, size := range [][2]int{{930, 690}, {1180, 790}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			peaks := make([]timeline.Peak, 30*48000/timeline.PeakSamples)
			for i := range peaks {
				peak := int16(500 + 3500*math.Abs(math.Sin(float64(i)/31)*math.Cos(float64(i)/83)))
				peaks[i] = timeline.Peak{Min: -peak, Max: peak}
			}
			info := media.Info{Duration: 90 * timeline.Second, Rate: 48000, Channels: 2, Video: true}
			item := &asset{name: "录制.mp4", path: "recording.mp4", analyzing: true}
			a := &app{assets: []*asset{item}, selected: item, cancel: func() {}, job: "过滤", opts: filterOptions{preset: "中等"}}
			view := ui.NewTester(a.view, size[0], size[1])
			if size[0] == 1180 {
				view.SetPreferences(ui.Preferences{ReduceMotion: true})
			}
			if !view.HasText("正在分析素材…") {
				t.Fatal("missing initial analysis status")
			}
			item.pendingWave = &analysisWave{info: info, wave: timeline.BuildWaveform(peaks)}
			a.progress = .5
			view.Frame()
			view.Advance(revealDuration)
			wave, ok := view.Find("正在分析的音频波形")
			if !ok || wave.W <= 0 || wave.H <= 0 || wave.Y+wave.H > float32(size[1]) {
				t.Fatalf("opening waveform lies outside the editor: %v", wave)
			}
			pixel := view.Image().RGBAAt(int(wave.X+wave.W/2), int(wave.Y+wave.H/2))
			if pixel.G < 100 {
				t.Fatalf("opening waveform was not drawn at the center: %v", pixel)
			}
			view.ClickAt(wave.X+wave.W/2, wave.Y+wave.H/2)
			if a.position != 0 || len(a.snapshot("").Assets[0].Clips) != 0 || a.snapshot("").Assets[0].Info.Duration != 0 {
				t.Fatal("pending waveform changed the edit or project")
			}
			if folder := os.Getenv("SILENCECUT_TEST_CAPTURE"); folder != "" {
				saveView(t, view, filepath.Join(folder, fmt.Sprintf("analyzing-waveform-%d.png", size[0])))
			}
			item.info, item.waveform, item.pendingWave = info, item.pendingWave.wave, nil
			item.analyzing, a.cancel = false, nil
			view.Frame()
			if _, ok := view.Find("音频波形和播放位置"); !ok {
				t.Fatal("the completed waveform is missing")
			}
		})
	}
}

func TestWorkspaceLayoutAndEdits(t *testing.T) {
	for _, size := range [][2]int{{930, 690}, {1120, 790}, {1180, 790}, {1600, 900}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			a := &app{opts: filterOptions{preset: "慢"}}
			applied, err := a.opts.settings(0)
			if err != nil {
				t.Fatal(err)
			}
			view := ui.NewTester(a.view, size[0], size[1])
			if !view.HasText("拖入音视频") {
				t.Fatal("empty workspace lacks its drop target")
			}
			if view.HasText("就绪") {
				t.Fatal("idle workspace still reserves a status bar")
			}
			if folder := os.Getenv("SILENCECUT_TEST_CAPTURE"); folder != "" {
				saveView(t, view, filepath.Join(folder, fmt.Sprintf("empty-%d.png", size[0])))
			}
			item := &asset{
				path: "超长的测试素材名称用于检查列表行是否在窄窗口中正确省略.wav",
				name: "超长的测试素材名称用于检查列表行是否在窄窗口中正确省略.wav",
				info: media.Info{Duration: 5 * timeline.Second, Rate: 48000, Channels: 1}, settings: applied,
				history: timeline.History{Clips: []timeline.Clip{
					{Span: timeline.Span{Start: 0, End: 3 * timeline.Second}, Keep: true},
					{Span: timeline.Span{Start: 3 * timeline.Second, End: 5 * timeline.Second}, Keep: false},
				}},
			}
			for i := range 937 {
				level := (math.Abs(math.Sin(float64(i)/20)) + math.Abs(math.Sin(float64(i)/41))) / 2
				if i >= 565 {
					level /= 30
				}
				item.peaks = append(item.peaks, timeline.Peak{Min: -int16(level * 25000), Max: int16(level * 26000)})
			}
			item.waveform = timeline.BuildWaveform(item.peaks)
			a.assets, a.selected = []*asset{item}, item
			view.Frame()
			settings := "重新过滤"
			if size[0] < wideWorkspace {
				settings = "过滤设置"
			}
			for _, label := range []string{"打开项目", "保存项目", "导出合并媒体", settings, "音频波形和播放位置", "移除", "撤销"} {
				r, ok := view.Find(label)
				if !ok || r.X < 0 || r.Y < 0 || r.W <= 0 || r.H <= 0 ||
					r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
					t.Fatalf("control %q is not fully visible: %+v", label, r)
				}
			}
			if folder := os.Getenv("SILENCECUT_TEST_CAPTURE"); folder != "" {
				saveView(t, view, filepath.Join(folder, fmt.Sprintf("workspace-%d.png", size[0])))
			}
			wave, _ := view.Find("音频波形和播放位置")
			view.ClickAt(wave.X+wave.W/4, wave.Y+wave.H/2)
			if a.position < timeline.Second || a.position > 1500000 {
				t.Fatalf("waveform click sought to %d", a.position)
			}
			if err := view.Click("移除"); err != nil || len(timeline.Kept(item.history.Clips)) != 0 {
				t.Fatalf("remove clip: %v, %+v", err, item.history)
			}
			if err := view.Click("撤销"); err != nil || len(timeline.Kept(item.history.Clips)) != 1 {
				t.Fatalf("undo: %v, %+v", err, item.history)
			}
			view.SetScale(2)
			if size[0] < wideWorkspace {
				if err := view.Click("过滤设置"); err != nil {
					t.Fatal(err)
				}
				view.Advance(revealDuration + 20*time.Millisecond)
				if folder := os.Getenv("SILENCECUT_TEST_CAPTURE"); folder != "" {
					saveView(t, view, filepath.Join(folder, fmt.Sprintf("settings-%d.png", size[0])))
				}
			}
			if err := view.Click("自定义"); err != nil {
				t.Fatal(err)
			}
			view.Advance(revealDuration + 20*time.Millisecond)
			for _, label := range []string{"静音阈值 (dB)", "片段后留白 (秒)", "应用设置", "试听模式", "放大波形"} {
				r, ok := view.Find(label)
				if !ok || r.W <= 0 || r.H <= 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
					t.Fatalf("control %q is not visible at 200%% DPI: %+v", label, r)
				}
			}
			if folder := os.Getenv("SILENCECUT_TEST_CAPTURE"); folder != "" {
				saveView(t, view, filepath.Join(folder, fmt.Sprintf("pending-%d.png", size[0])))
			}
			if size[0] < wideWorkspace {
				view.Key(0, ui.KeyEscape)
				if _, open := view.Find("应用设置"); open || !view.Focused("过滤设置") {
					t.Fatal("closing settings must return focus to the trigger")
				}
			}
			if err := view.Click("原始"); err != nil || !a.originalPreview {
				t.Fatalf("original view action: %v", err)
			}
			if err := view.Click("过滤后"); err != nil || a.originalPreview {
				t.Fatalf("filtered view action: %v", err)
			}
		})
	}
}

func saveView(t *testing.T, view *ui.Tester, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = png.Encode(f, view.Image())
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("capture: %v %v", err, closeErr)
	}
}

func TestTopbarClearsNativeWindowControls(t *testing.T) {
	item := &asset{name: "项目旁白.wav", info: media.Info{Duration: 5 * timeline.Second}}
	a := &app{assets: []*asset{item}, selected: item, opts: filterOptions{preset: "慢"}}
	view := ui.NewTester(a.view, 930, 690)
	exportButton := func(disabled bool) {
		t.Helper()
		button, ok := view.AccessByRole(ui.RoleButton, "导出合并媒体")
		if !ok || button.Disabled != disabled || button.Bounds.H != controlHeight {
			t.Fatalf("export disabled=%t: %+v", disabled, button)
		}
	}
	exportButton(true)
	item.history.Clips = []timeline.Clip{{Span: timeline.Span{End: item.info.Duration}, Keep: true}}
	view.Frame()
	exportButton(false)
	a.picking = true
	view.Frame()
	exportButton(true)
	a.picking = false
	for _, tc := range []struct {
		name string
		bar  ui.TitleBar
	}{
		{"windows", ui.TitleBar{Height: float32(topBarHeight), Right: 138}},
		{"macos", ui.TitleBar{Height: float32(topBarHeight), Left: 70, Bottom: 28}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view.SetTitleBar(tc.bar)
			view.Frame()
			for _, label := range []string{"打开项目", "保存项目", "导出合并媒体"} {
				r, ok := view.Find(label)
				if !ok || r.W <= 0 || r.H <= 0 {
					t.Fatalf("%s is not visible: %+v", label, r)
				}
				if r.X < tc.bar.Left || r.X+r.W > float32(930)-tc.bar.Right {
					t.Fatalf("%s overlaps native window controls: %+v, bar %+v", label, r, tc.bar)
				}
			}
		})
	}
}

func TestTopbarActionsStayInsidePanelColumns(t *testing.T) {
	const width, height = 1600, 900
	item := &asset{name: "项目旁白.wav", info: media.Info{Duration: 5 * timeline.Second}}
	projectPath := filepath.Join(t.TempDir(), "一个非常长的项目名称用于检查顶栏项目区不会挤过分界线.silencecut")
	a := &app{
		assets: []*asset{item}, selected: item, projectPath: projectPath, projectDirty: true,
		opts: filterOptions{preset: "慢"},
	}
	view := ui.NewTester(a.view, width, height)
	if view.HasText(filepath.Base(projectPath)) {
		t.Fatal("project name is still visible")
	}
	if dirty, ok := view.Find("未保存"); !ok || dirty.X+dirty.W > sideWidth {
		t.Fatalf("unsaved status crosses the material rail: %+v", dirty)
	}
	for _, label := range []string{"打开项目", "保存项目"} {
		r, ok := view.Find(label)
		if !ok || r.X < sideWidth+1 {
			t.Fatalf("%s is not fully inside the editor column: %+v", label, r)
		}
	}
	if err := view.RightClick("保存项目"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(view.Menu(), []string{"另存为…"}) {
		t.Fatalf("save menu = %q", view.Menu())
	}
	view.CloseMenu()
	if r, ok := view.Find("导出合并媒体"); !ok || r.X < width-settingsWidth || r.X+r.W > width {
		t.Fatalf("export button is not above the inspector column: %+v", r)
	}
	a.projectPath = ""
	view.Frame()
	if view.HasText("未命名项目") {
		t.Fatal("untitled project name is still visible")
	}
}

func TestEmptyTopbarOmitsAppName(t *testing.T) {
	a := &app{opts: filterOptions{preset: "慢"}}
	view := ui.NewTester(a.view, 1180, 790)
	for _, removed := range []string{title, "专注模式", "导出切片"} {
		if view.HasText(removed) {
			t.Fatalf("removed topbar item %q is still visible", removed)
		}
	}
	save, ok := view.AccessByRole(ui.RoleButton, "保存项目")
	if !ok || !strings.Contains(save.Description, "另存为") || !strings.Contains(save.Description, "Shift+S") {
		t.Fatalf("save-as shortcut is not exposed: %+v", save)
	}
}

func TestInspectorPendingActionKeepsLayout(t *testing.T) {
	settings, err := (filterOptions{preset: "慢"}).settings(-40)
	if err != nil {
		t.Fatal(err)
	}
	item := &asset{
		name: "口播.wav", info: media.Info{Duration: 5 * timeline.Second}, automatic: -40, settings: settings,
		history: timeline.History{Clips: []timeline.Clip{{Span: timeline.Span{End: 5 * timeline.Second}, Keep: true}}},
	}
	a := &app{assets: []*asset{item}, selected: item, opts: filterOptions{preset: "慢"}}
	view := ui.NewTester(a.view, 1180, 790)
	before, ok := view.Find("重新过滤")
	if !ok {
		t.Fatal("rerun action is missing")
	}
	if err := view.Click("快"); err != nil {
		t.Fatal(err)
	}
	after, ok := view.Find("应用设置")
	if !ok {
		t.Fatal("pending settings must be reflected by the action")
	}
	if before != after {
		t.Fatalf("pending state moved the action: before %+v after %+v", before, after)
	}
	if view.HasText("设置待应用") {
		t.Fatal("pending state must not insert a separate layout row")
	}
	if view.HasText("切片仅导出保留段") {
		t.Fatal("clip export option must stay removed")
	}
}

type dragPreview struct {
	playing bool
	seeks   []int64
}

func (p *dragPreview) Playing() bool { return p.playing }
func (p *dragPreview) Play()         { p.playing = true }
func (p *dragPreview) Pause()        { p.playing = false }
func (p *dragPreview) Close()        {}
func (p *dragPreview) Position() time.Duration {
	return time.Duration(p.seeks[len(p.seeks)-1]) * time.Microsecond
}
func (p *dragPreview) Seek(at time.Duration) { p.seeks = append(p.seeks, at.Microseconds()) }

func TestPlayheadAndBoundaryGestures(t *testing.T) {
	const second = timeline.Second
	item := &asset{info: media.Info{Duration: 30 * second}, history: timeline.History{Clips: []timeline.Clip{
		{Span: timeline.Span{Start: 0, End: 10 * second}, Keep: true},
		{Span: timeline.Span{Start: 10 * second, End: 15 * second}},
		{Span: timeline.Span{Start: 15 * second, End: 30 * second}, Keep: true},
	}}}
	p := &dragPreview{seeks: []int64{10 * second}}
	selection := timeline.Span{Start: 7 * second, End: 8 * second}
	a := &app{selected: item, player: p, position: 10 * second, selection: selection, viewStart: 5 * second, viewLength: 15 * second}
	view := ui.NewTester(a.view, 1180, 790)
	wave, _ := view.Find("音频波形和播放位置")
	x := func(s float32) float32 { return wave.X + wave.W*(s-5)/15 }
	near := func(at, expected int64) bool { return at >= expected-2 && at <= expected+2 }
	body, ruler, handle := wave.Y+wave.H/2, wave.Y+3, wave.Y+rulerHeight+4
	view.Move(x(10)+5, body)
	if view.Cursor() != ui.CursorGrab {
		t.Fatalf("line cursor = %v", view.Cursor())
	}
	view.Move(x(10), handle)
	if view.Cursor() != ui.CursorResizeEW {
		t.Fatalf("overlapping boundary cursor = %v", view.Cursor())
	}
	view.Move(x(10)+3, ruler)
	if view.Cursor() != ui.CursorGrab {
		t.Fatalf("ruler handle cursor = %v", view.Cursor())
	}
	p.Play()
	view.Press(x(10)+3, ruler)
	if p.Playing() || a.position != 10*second || a.selection != selection || a.waveDrag.kind != wavePlayhead {
		t.Fatalf("press moved the playhead, lost selection or failed to pause: %+v", a.waveDrag)
	}
	view.Move(x(11)+3, handle)
	view.Move(x(12)+3, body)
	if len(p.seeks) != 2 || !near(a.position, 12*second) {
		t.Fatalf("move was not immediate or preview was not throttled: position=%d seeks=%v", a.position, p.seeks)
	}
	view.Advance(33 * time.Millisecond)
	view.Move(x(13)+3, body)
	if len(p.seeks) != 3 {
		t.Fatalf("preview did not seek after 33 ms: %v", p.seeks)
	}
	view.Release(x(14)+3, body)
	if !near(a.position, 14*second) || p.seeks[len(p.seeks)-1] != a.position || p.Playing() || a.selection != selection || item.history.CanUndo() {
		t.Fatalf("playhead release: position=%d seeks=%v selection=%v", a.position, p.seeks, a.selection)
	}
	view.Press(x(14), body)
	view.Move(wave.X-20, wave.Y-40)
	if a.position != 5*second {
		t.Fatalf("left clamp = %d", a.position)
	}
	view.Move(wave.X+wave.W+20, wave.Y-40)
	if a.position != 20*second {
		t.Fatalf("right clamp = %d", a.position)
	}
	view.Key(0, ui.KeyEscape)
	view.Move(x(17), body)
	view.Release(x(17), body)
	if !near(a.position, 14*second) || a.selection != selection || p.seeks[len(p.seeks)-1] != a.position {
		t.Fatalf("Escape did not restore playhead: position=%d selection=%v", a.position, a.selection)
	}
	view.Press(x(14), body)
	view.Release(wave.X+wave.W+20, body)
	if a.position != 20*second || p.seeks[len(p.seeks)-1] != a.position {
		t.Fatalf("release outside: position=%d seeks=%v", a.position, p.seeks)
	}
	view.ClickAt(x(10), body)
	view.Press(x(10), handle)
	view.Move(x(11), handle)
	if item.history.CanUndo() || !near(a.waveDrag.end, 11*second) {
		t.Fatal("boundary move must preview without editing")
	}
	view.Release(x(12), handle)
	if !near(item.history.Clips[0].End, 12*second) {
		t.Fatalf("boundary release = %d", item.history.Clips[0].End)
	}
	a.history(false)
	if item.history.Clips[0].End != 10*second || item.history.CanUndo() {
		t.Fatal("boundary release must create one undo step")
	}
	a.selection = selection
	view.Press(x(16), body)
	view.Move(x(17), body)
	if draft := a.waveDrag.draft; !near(draft.Start, 16*second) || !near(draft.End, 17*second) || a.selection != selection {
		t.Fatalf("blank area did not preview a selection: draft=%v committed=%v", draft, a.selection)
	}
	view.Key(0, ui.KeyEscape)
	view.Release(x(17), body)
	if a.selection != selection || !near(a.position, 12*second) {
		t.Fatal("Escape did not restore the prior selection and position")
	}
	view.Press(x(10), handle)
	view.Move(x(11), handle)
	view.Key(0, ui.KeyEscape)
	view.Release(x(11), handle)
	if item.history.CanUndo() || item.history.Clips[0].End != 10*second || a.selection != selection {
		t.Fatal("Escape committed a boundary edit or changed the selection")
	}

	item.info.Video = true
	view.Frame()
	if err := view.Click("播放"); err != nil || !p.Playing() || a.position != 15*second ||
		p.seeks[len(p.seeks)-1] != 15*second {
		t.Fatalf("filtered playback did not map the cut: %v, cursor %d, seeks %v", err, a.position, p.seeks)
	}
	a.stopPlayback()
	a.seek(12 * second)
	view.Frame()
	if err := view.Click("原始"); err != nil {
		t.Fatal(err)
	}
	if err := view.Click("播放"); err != nil || !p.Playing() || a.position != 12*second {
		t.Fatalf("original playback did not start within the removed interval: %v, %d", err, a.position)
	}
}

func TestPlayheadDragPansTimelineAtEdges(t *testing.T) {
	const second = timeline.Second
	duration := int64(120) * second
	item := &asset{info: media.Info{Duration: duration}}
	a := &app{selected: item, position: 35 * second, viewStart: 20 * second, viewLength: 30 * second}
	view := ui.NewTester(a.view, 1180, 790)
	wave, _ := view.Find("音频波形和播放位置")
	body := wave.Y + wave.H/2
	screenX := func(at int64) float32 {
		return wave.X + wave.W*float32(float64(at-a.viewStart)/float64(a.detailLength(duration)))
	}

	view.Press(screenX(35*second), body)
	view.Move(wave.X+wave.W+16, body)
	view.Advance(400 * time.Millisecond)
	if a.viewStart <= 20*second {
		t.Fatalf("right-edge drag did not advance the timeline: %d µs", a.viewStart)
	}
	if a.position < a.viewStart+a.detailLength(duration)-second {
		t.Fatalf("playhead did not stay at the advancing right edge: view %d, position %d", a.viewStart, a.position)
	}
	view.Release(wave.X+wave.W+16, body)

	a.viewStart, a.position = 60*second, 75*second
	view.Frame()
	view.Press(screenX(75*second), body)
	view.Move(wave.X-16, body)
	view.Advance(400 * time.Millisecond)
	if a.viewStart >= 60*second {
		t.Fatalf("left-edge drag did not rewind the timeline: %d µs", a.viewStart)
	}
	if a.position > a.viewStart+second {
		t.Fatalf("playhead did not stay at the advancing left edge: view %d, position %d", a.viewStart, a.position)
	}
	view.Key(0, ui.KeyEscape)
	view.Release(wave.X-16, body)
	if a.viewStart != 60*second || a.position != 75*second {
		t.Fatalf("Escape did not restore the drag start: view %d, position %d", a.viewStart, a.position)
	}
}

func TestWaveSelectionManipulation(t *testing.T) {
	const s = timeline.Second
	item := &asset{info: media.Info{Duration: 30 * s}, history: timeline.History{Clips: []timeline.Clip{
		{Span: timeline.Span{End: 10 * s}, Keep: true},
		{Span: timeline.Span{Start: 10 * s, End: 30 * s}},
	}}}
	p := &dragPreview{seeks: []int64{10 * s}}
	a := &app{selected: item, player: p, position: 10 * s}
	view := ui.NewTester(a.view, 1180, 790)
	wave, _ := view.Find("音频波形和播放位置")
	x := func(at float32) float32 { return wave.X + wave.W*at/30 }
	body, ruler, boundary := wave.Y+wave.H/2, wave.Y+3, wave.Y+rulerHeight+4
	assertSpan := func(got timeline.Span, start, end int64) {
		t.Helper()
		if math.Abs(float64(got.Start-start)) > 5 || math.Abs(float64(got.End-end)) > 5 {
			t.Fatalf("selection = %+v, want [%d, %d)", got, start, end)
		}
	}
	drag := func(from, to, y float32) {
		view.Press(x(from), y)
		view.Release(x(to), y)
	}

	view.Press(x(20), body)
	view.Move(x(16), body)
	assertSpan(a.waveDrag.draft, 16*s, 20*s)
	assertSpan(a.selection, 0, 0)
	if a.position != 10*s || len(p.seeks) != 1 {
		t.Fatalf("selection preview moved or sought the playhead: %d %v", a.position, p.seeks)
	}
	view.Release(x(15), body)
	assertSpan(a.selection, 15*s, 20*s)
	drag(15, 12, body)
	drag(20, 22, body)
	assertSpan(a.selection, 12*s, 22*s)
	drag(16, 24, body)
	assertSpan(a.selection, 20*s, 30*s)
	view.Press(x(25), body)
	view.Release(wave.X-wave.W, body)
	assertSpan(a.selection, 0, 10*s)
	if a.position != 10*s || len(p.seeks) != 1 || item.history.CanUndo() {
		t.Fatalf("selection changed playback or history: %d %v", a.position, p.seeks)
	}
	view.Move(x(10), body)
	if view.Cursor() != ui.CursorResizeEW {
		t.Fatalf("selection end overlapping playhead: %v", view.Cursor())
	}
	view.Press(x(10), boundary)
	if a.waveDrag.kind != waveBoundary {
		t.Fatalf("clip trim lane lost to selection end: %v", a.waveDrag.kind)
	}
	view.CancelPress()

	view.Press(x(5), body)
	view.Release(x(5)+1, body)
	assertSpan(a.selection, 0, 10*s)
	if a.position <= 5*s || a.position >= 5*s+100_000 {
		t.Fatalf("small movement did not click at release position: %d", a.position)
	}
	view.ClickAt(x(12), ruler)
	assertSpan(a.selection, 0, 10*s)
	if math.Abs(float64(a.position-12*s)) > 5 {
		t.Fatalf("ruler did not locate: %d", a.position)
	}
	view.Key(0, ui.KeyEscape)
	assertSpan(a.selection, 0, 0)

	drag(14, 20, body)
	assertSpan(a.selection, 14*s, 20*s)
	drag(14, 22, body)
	if a.selection.End-a.selection.Start != 1 {
		t.Fatalf("resize crossed the fixed endpoint: %+v", a.selection)
	}
	view.Press(x(20)-2, body)
	if a.waveDrag.kind != waveSelectStart {
		t.Fatalf("start grip of a short selection is unreachable: %v", a.waveDrag.kind)
	}
	view.Release(x(17), body)
	// The short selection offsets its grips so both remain reachable.
	if a.selection.Start < 17*s || a.selection.Start > 17*s+200_000 {
		t.Fatalf("short selection could not be resized: %+v", a.selection)
	}
	selected := a.selection
	view.Press(x(20), body)
	view.Move(x(24), body)
	if export, ok := a.timelineSelection(item); !ok || export != selected {
		t.Fatalf("export saw an unfinished resize: %+v", export)
	}
	view.Release(x(25), body)
	span, ok := a.timelineSelection(item)
	if !ok || span.Start != selected.Start || math.Abs(float64(span.End-25*s)) > 5 {
		t.Fatalf("export did not see the final resize: %+v", span)
	}
	if err := view.Click("保留"); err != nil || len(item.history.Clips) != 4 ||
		item.history.Clips[2].Span != span || !item.history.Clips[2].Keep {
		t.Fatalf("keep did not apply the adjusted range: %v %+v", err, item.history.Clips)
	}
	view.ClickAt(x(28), body)
	assertSpan(a.selection, 0, 0)
}

func TestWaveSelectionEdgePanAndOverview(t *testing.T) {
	const s = timeline.Second
	item := &asset{info: media.Info{Duration: 120 * s}}
	selection := timeline.Span{Start: 25 * s, End: 30 * s}
	a := &app{selected: item, position: 33 * s, selection: selection, viewStart: 20 * s, viewLength: 30 * s}
	view := ui.NewTester(a.view, 1180, 790)
	wave, _ := view.Find("音频波形和播放位置")
	y := wave.Y + wave.H/2
	view.Press(wave.X+wave.W/3, y)
	view.Move(wave.X+wave.W+10, y)
	view.Advance(400 * time.Millisecond)
	if a.viewStart <= 20*s || a.waveDrag.draft.End <= 50*s {
		t.Fatalf("selection end did not follow the panning edge: %d %+v", a.viewStart, a.waveDrag.draft)
	}
	view.CancelPress()
	if a.viewStart != 20*s || a.position != 33*s || a.selection != selection {
		t.Fatalf("system cancel did not restore the timeline: %d %d %+v", a.viewStart, a.position, a.selection)
	}
	overview, _ := view.Find("全局波形")
	x := func(at float32) float32 { return overview.X + overview.W*at/120 }
	y = overview.Y + overview.H/2
	view.Press(x(80), y)
	view.Release(x(90), y)
	if math.Abs(float64(a.viewStart-75*s)) > 20 || a.position != 33*s || a.selection != selection {
		t.Fatalf("overview drag unexpectedly sought or cleared selection: %d %d %+v", a.viewStart, a.position, a.selection)
	}
	view.Press(x(90), y)
	view.Move(x(105), y)
	view.Key(0, ui.KeyEscape)
	view.Release(x(105), y)
	if math.Abs(float64(a.viewStart-75*s)) > 20 || a.position != 33*s {
		t.Fatalf("overview Escape did not restore the viewport: %d %d", a.viewStart, a.position)
	}
	view.ClickAt(x(40), y)
	if math.Abs(float64(a.position-40*s)) > 20 || math.Abs(float64(a.viewStart-25*s)) > 20 || a.selection != selection {
		t.Fatalf("overview click did not center without clearing selection: %d %d %+v", a.position, a.viewStart, a.selection)
	}
}

func TestKeyboardBoundaryNudging(t *testing.T) {
	const second = timeline.Second
	item := &asset{info: media.Info{Duration: 30 * second}, history: timeline.History{Clips: []timeline.Clip{
		{Span: timeline.Span{Start: 0, End: 10 * second}, Keep: true},
		{Span: timeline.Span{Start: 10 * second, End: 15 * second}},
		{Span: timeline.Span{Start: 15 * second, End: 30 * second}, Keep: true},
	}}}
	a := &app{selected: item, position: 12 * second, viewStart: 5 * second, viewLength: 15 * second}
	view := ui.NewTester(a.view, 1180, 790)
	wave, _ := view.Find("音频波形和播放位置")
	x := func(s float32) float32 { return wave.X + wave.W*(s-5)/15 }
	view.ClickAt(x(12), wave.Y+wave.H/2)

	view.Key(ui.Alt, ui.KeyRight)
	if item.history.Clips[0].End != 10*second+boundaryNudge || a.position != 10*second+boundaryNudge {
		t.Fatalf("start boundary nudge = %+v at %d", item.history.Clips[:2], a.position)
	}
	view.Key(ui.Alt|ui.Shift, ui.KeyLeft)
	if item.history.Clips[1].End != 15*second-boundaryNudge {
		t.Fatalf("end boundary nudge = %+v", item.history.Clips[:3])
	}
	a.history(false)
	if item.history.Clips[1].End != 15*second {
		t.Fatalf("keyboard boundary edit was not undoable: %+v", item.history.Clips[:3])
	}
}

func TestSettingsDraftAndRefilterConfirmation(t *testing.T) {
	clips := []timeline.Clip{{Span: timeline.Span{End: 5 * timeline.Second}, Keep: true}}
	first := &asset{name: "第一段.wav", path: "first", info: media.Info{Duration: 5 * timeline.Second}, filtered: clips,
		history: timeline.History{Clips: clips}}
	second := &asset{name: "第二段.wav", path: "second", filtered: clips, history: timeline.History{Clips: clips}}
	a := &app{assets: []*asset{first, second}, selected: first, position: 700_000, viewLength: 2 * timeline.Second,
		selection: timeline.Span{Start: 1, End: 900_000}, opts: filterOptions{preset: "自定义", level: "-40", before: "错", after: "0.1"}}
	view := ui.NewTesterWith(a.view, 930, 690, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	click := func(label string) {
		t.Helper()
		if err := view.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	click("过滤设置")
	click("应用设置")
	if !view.HasText("请输入 0 到 10 秒") || view.HasText("请输入 −70 到 20 dB") {
		t.Fatal("expected only the invalid field's message")
	}
	selection := a.selection
	view.SetSize(wideWorkspace, 690)
	if _, ok := view.Find("应用设置"); !ok || a.settingsOpen || a.opts.before != "错" ||
		a.position != 700_000 || a.selection != selection || a.viewLength != 2*timeline.Second {
		t.Fatal("crossing the breakpoint changed the draft or editor state")
	}
	view.SetSize(wideWorkspace-1, 690)
	if _, ok := view.Find("应用设置"); ok {
		t.Fatal("settings rail was still present below the persistent breakpoint")
	}
	a.opts.before = "0.2"
	first.history.Edit(timeline.Span{End: timeline.Second}, false)
	click("过滤设置")
	click("应用到全部")
	if !view.HasText("重新过滤会覆盖 1 个素材的手工编辑。") || len(a.refilter) != 2 {
		t.Fatalf("incorrect scope for confirmation: %d affected, %d requested", a.refilterCount, len(a.refilter))
	}
	view.Key(0, ui.KeyEscape)
	if len(a.refilter) != 0 || slices.Equal(first.history.Clips, clips) || !slices.Equal(second.history.Clips, clips) {
		t.Fatal("dismissing confirmation did not preserve edits")
	}
}

func TestInvalidImportSettingsOpensInspector(t *testing.T) {
	a := &app{opts: filterOptions{preset: "自定义", before: "0.10", after: "0.10"}}
	view := ui.NewTester(a.view, 1180, 790)
	a.add([]string{"sample.wav"})
	view.Frame()
	if len(a.assets) != 0 || !view.HasText("请输入 −70 到 20 dB") ||
		!view.HasText("请修正过滤设置 · 请输入 −70 到 20 dB") {
		t.Fatal("invalid settings must be visible after a rejected import")
	}
}

func TestProjectSourceRowsAndResume(t *testing.T) {
	first := &asset{path: "same.wav", name: "same.wav"}
	second := &asset{path: first.path, name: first.name, offline: true, pendingLoad: true}
	a := &app{assets: []*asset{first, second}, selected: first, opts: filterOptions{preset: "中等"}}
	view := ui.NewTester(a.view, 1180, 790)
	if !view.HasText("继续读取素材") {
		t.Fatal("resume must be visible with a loaded source selected")
	}
	a.selectAsset(second)
	view.Frame()
	if view.HasText("重新定位…") {
		t.Fatal("a pending source should keep its original path when resumed")
	}
	second.pendingLoad = false
	view.Frame()
	if view.HasText("继续读取素材") || !view.HasText("重新定位…") {
		t.Fatal("a checked, offline source should offer relinking")
	}
}

func TestBatchStatusAndPublishedFolder(t *testing.T) {
	item := &asset{name: "失败.wav", info: media.Info{Duration: 5 * timeline.Second},
		history: timeline.History{Clips: []timeline.Clip{{Span: timeline.Span{End: 5 * timeline.Second}, Keep: true}}}}
	a := &app{outputDir: t.TempDir(), assets: []*asset{item}, selected: item}
	view := ui.NewTester(a.view, 1180, 790)
	if view.HasText("打开导出文件夹") {
		t.Fatal("choosing a folder must not count as writing files")
	}
	ctx := a.begin("导出")
	view.Frame()
	if !view.HasText("正在导出") {
		t.Fatal("missing running export status")
	}
	if err := view.Click("取消"); err != nil || ctx.Err() != context.Canceled || !view.HasText("正在取消导出") {
		t.Fatalf("missing canceling status: %v, %v", err, ctx.Err())
	}
	item.exportIssue = "文件丢失"
	a.end(batchStatus("导出", "已导出 0 个文件", 0, 1, "可查看错误详情", false))
	view.Frame()
	if !view.HasText("导出失败 · 已导出 0 个文件 · 1 个素材失败 · 可查看错误详情") ||
		!view.HasText("导出失败 · 文件丢失") || view.HasText("打开导出文件夹") ||
		!view.HasText("查看错误详情") {
		t.Fatal("failed export did not show the failure or exposed the folder prematurely")
	}
	a.publishedDir = a.outputDir
	item.exportIssue = ""
	a.status = batchStatus("导出", "已导出 1 个文件", 1, 1, "可查看错误详情", false)
	view.Frame()
	if !view.HasText("导出部分成功 · 已导出 1 个文件 · 1 个素材失败 · 可查看错误详情") || !view.HasText("打开导出文件夹") {
		t.Fatal("partial export did not expose the result status and folder")
	}
}

func TestAssetErrorReport(t *testing.T) {
	a := &app{assets: []*asset{
		{name: "待读取.wav", issue: "待读取素材", pendingLoad: true},
		{name: "失败.wav", issue: "解码失败", exportIssue: "目标目录不可写"},
	}}
	count, detail := a.errorReport()
	if count != 1 || !strings.Contains(detail, "失败.wav") ||
		!strings.Contains(detail, "处理失败 · 解码失败") ||
		!strings.Contains(detail, "导出失败 · 目标目录不可写") ||
		strings.Contains(detail, "待读取.wav") {
		t.Fatalf("error report = %d\n%s", count, detail)
	}
}

func TestErrorDetailsStayVisibleAtMinimumSize(t *testing.T) {
	item := &asset{name: "失败.wav", exportIssue: "目标目录不可写"}
	a := &app{
		assets: []*asset{item}, selected: item, opts: filterOptions{preset: "中等"},
		status: "导出失败 · 已导出 0 个文件 · 1 个素材失败 · 可查看错误详情", publishedDir: t.TempDir(),
	}
	view := ui.NewTester(a.view, 930, 690)
	for _, label := range []string{"查看错误详情", "打开导出文件夹"} {
		r, ok := view.Find(label)
		if !ok || r.X < 0 || r.X+r.W > 930 || r.Y < 0 || r.Y+r.H > 690 {
			t.Fatalf("%q is outside the minimum window: %+v", label, r)
		}
	}
}

func TestOriginalVideoPreviewError(t *testing.T) {
	item := &asset{info: media.Info{Duration: 2 * timeline.Second, Video: true}}
	a := &app{
		assets: []*asset{item}, selected: item, opts: filterOptions{preset: "慢"},
		previewIssue: "无法播放原片",
	}
	view := ui.NewTester(a.view, 930, 690)
	if !view.HasText("无法播放原片") {
		t.Fatal("source video failure has no feedback")
	}
	if _, ok := view.Find("重试预览"); !ok {
		t.Fatal("source video failure has no retry action")
	}
}

func TestAssetContextMenus(t *testing.T) {
	first := &asset{path: "first.wav", name: "第一段.wav"}
	second := &asset{path: "second.wav", name: "第二段.wav"}
	a := &app{assets: []*asset{first, second}, selected: second, opts: filterOptions{preset: "中等"}}
	view := ui.NewTester(a.view, 1180, 790)
	want := []string{"在文件夹中显示", "复制路径", "-", "重新过滤", "-", "从列表移除"}

	if err := view.RightClick(first.name); err != nil {
		t.Fatal(err)
	}
	if a.selected != first || !slices.Equal(view.Menu(), want) {
		t.Fatalf("right-click selected %v and opened %q, want %q", a.selected, view.Menu(), want)
	}
	view.CloseMenu()

	if err := view.Click(second.name); err != nil || a.selected != second {
		t.Fatalf("select second row: %v, selected %v", err, a.selected)
	}
	view.Key(0, ui.KeyContextMenu)
	if !slices.Equal(view.Menu(), want) {
		t.Fatalf("menu key opened %q, want %q", view.Menu(), want)
	}
	view.CloseMenu()

	if err := view.Click(first.name); err != nil {
		t.Fatal(err)
	}
	view.Key(ui.Shift, ui.KeyF10)
	if !slices.Equal(view.Menu(), want) {
		t.Fatalf("Shift+F10 opened %q, want %q", view.Menu(), want)
	}
	if err := view.ChooseMenuItem("从列表移除"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.assets, []*asset{second}) {
		t.Fatalf("the first row's menu removed %v", a.assets)
	}
}

func TestAssetContextMenuStates(t *testing.T) {
	checkDisabled := func(t *testing.T, view *ui.Tester, label string) {
		t.Helper()
		if err := view.ChooseMenuItem(label); err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("%q: %v, want disabled", label, err)
		}
	}

	t.Run("busy loaded", func(t *testing.T) {
		item := &asset{path: "busy.wav", name: "忙碌.wav"}
		a := &app{assets: []*asset{item}, selected: item, picking: true, opts: filterOptions{preset: "中等"}}
		view := ui.NewTester(a.view, 1180, 790)
		if err := view.RightClick(item.name); err != nil {
			t.Fatal(err)
		}
		checkDisabled(t, view, "重新过滤")
		checkDisabled(t, view, "从列表移除")
		view.CloseMenu()
	})

	t.Run("offline", func(t *testing.T) {
		item := &asset{path: "offline.wav", name: "离线.wav", offline: true}
		a := &app{assets: []*asset{item}, selected: item, opts: filterOptions{preset: "中等"}}
		view := ui.NewTester(a.view, 1180, 790)
		if err := view.RightClick(item.name); err != nil {
			t.Fatal(err)
		}
		want := []string{"重新定位…", "-", "从列表移除"}
		if !slices.Equal(view.Menu(), want) {
			t.Fatalf("offline menu = %q, want %q", view.Menu(), want)
		}
		view.CloseMenu()

		a.picking = true
		if err := view.RightClick(item.name); err != nil {
			t.Fatal(err)
		}
		checkDisabled(t, view, "重新定位…")
		checkDisabled(t, view, "从列表移除")
		view.CloseMenu()
	})

	t.Run("pending", func(t *testing.T) {
		item := &asset{path: "pending.wav", name: "待读取.wav", offline: true, pendingLoad: true}
		a := &app{assets: []*asset{item}, selected: item, opts: filterOptions{preset: "中等"}}
		view := ui.NewTester(a.view, 1180, 790)
		if err := view.RightClick(item.name); err != nil {
			t.Fatal(err)
		}
		want := []string{"在文件夹中显示", "复制路径", "-", "从列表移除"}
		if !slices.Equal(view.Menu(), want) {
			t.Fatalf("pending menu = %q, want %q", view.Menu(), want)
		}
	})

	t.Run("error details", func(t *testing.T) {
		item := &asset{path: "failed.wav", name: "失败.wav", exportIssue: "无法写入目标目录"}
		a := &app{assets: []*asset{item}, selected: item, opts: filterOptions{preset: "中等"}}
		view := ui.NewTester(a.view, 1180, 790)
		if err := view.RightClick(item.name); err != nil {
			t.Fatal(err)
		}
		want := []string{"在文件夹中显示", "复制路径", "查看错误详情…", "-", "重新过滤", "-", "从列表移除"}
		if !slices.Equal(view.Menu(), want) {
			t.Fatalf("error menu = %q, want %q", view.Menu(), want)
		}
	})
}

func TestWaveContextMenu(t *testing.T) {
	const second = timeline.Second
	initial := []timeline.Clip{
		{Span: timeline.Span{End: 10 * second}, Keep: true},
		{Span: timeline.Span{Start: 10 * second, End: 15 * second}},
		{Span: timeline.Span{Start: 15 * second, End: 30 * second}, Keep: true},
	}
	item := &asset{info: media.Info{Duration: 30 * second, Video: true}, history: timeline.History{Clips: slices.Clone(initial)}}
	p := &dragPreview{seeks: []int64{18 * second}}
	a := &app{selected: item, player: p, position: 10 * second,
		selection: timeline.Span{Start: 7 * second, End: 8 * second}, viewStart: 5 * second, viewLength: 15 * second}
	view := ui.NewTester(a.view, 1180, 790)
	wave, _ := view.Find("音频波形和播放位置")
	x := func(at float32) float32 { return wave.X + wave.W*(at-5)/15 }
	body := wave.Y + wave.H/2
	selection := a.selection

	view.RightClickAt(x(18), body)
	if a.position < 18*second-40_000 || a.position > 18*second+40_000 || a.selection != selection {
		t.Fatalf("right-click moved to %d and changed selection to %v", a.position, a.selection)
	}
	wantSelected := []string{"从此处播放/暂停", "导出选中区域…", "-", "保留", "移除", "-", "撤销", "重做"}
	if !slices.Equal(view.Menu(), wantSelected) {
		t.Fatalf("selected menu = %q, want %q", view.Menu(), wantSelected)
	}
	if err := view.ChooseMenuItem("移除"); err != nil {
		t.Fatal(err)
	}
	i := clipAt(item.history.Clips, 7*second+1)
	if i >= len(item.history.Clips) || item.history.Clips[i].Keep ||
		item.history.Clips[i].Start != 7*second || item.history.Clips[i].End != 8*second {
		t.Fatalf("selection edit = %+v", item.history.Clips)
	}

	view.RightClickAt(x(18), body)
	if err := view.ChooseMenuItem("撤销"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(item.history.Clips, initial) {
		t.Fatalf("undo restored %+v, want %+v", item.history.Clips, initial)
	}
	view.RightClickAt(x(18), body)
	if err := view.ChooseMenuItem("重做"); err != nil {
		t.Fatal(err)
	}
	i = clipAt(item.history.Clips, 7*second+1)
	if i >= len(item.history.Clips) || item.history.Clips[i].Keep {
		t.Fatalf("redo = %+v", item.history.Clips)
	}
	view.RightClickAt(x(18), body)
	if err := view.ChooseMenuItem("撤销"); err != nil {
		t.Fatal(err)
	}

	a.selection = timeline.Span{}
	view.Frame()
	view.RightClickAt(x(18), body)
	wantCurrent := []string{"从此处播放/暂停", "-", "保留", "移除", "-", "撤销", "重做"}
	if !slices.Equal(view.Menu(), wantCurrent) {
		t.Fatalf("current-clip menu = %q, want %q", view.Menu(), wantCurrent)
	}
	if err := view.ChooseMenuItem("从此处播放/暂停"); err != nil || !p.Playing() {
		t.Fatalf("play from menu: %v, playing %t", err, p.Playing())
	}
	view.RightClickAt(x(18), body)
	if err := view.ChooseMenuItem("从此处播放/暂停"); err != nil || p.Playing() {
		t.Fatalf("pause from menu: %v, playing %t", err, p.Playing())
	}
	view.RightClickAt(x(18), body)
	if err := view.ChooseMenuItem("移除"); err != nil {
		t.Fatal(err)
	}
	i = clipAt(item.history.Clips, 18*second)
	if i >= len(item.history.Clips) || item.history.Clips[i].Keep {
		t.Fatalf("current clip edit = %+v", item.history.Clips)
	}
	view.RightClickAt(x(18), body)
	if err := view.ChooseMenuItem("撤销"); err != nil {
		t.Fatal(err)
	}

	view.ClickAt(x(17), body)
	afterClick := a.position
	view.Key(ui.Shift, ui.KeyF10)
	if a.position != afterClick || !slices.Equal(view.Menu(), wantCurrent) {
		t.Fatalf("keyboard menu moved %d to %d or opened %q", afterClick, a.position, view.Menu())
	}
	view.Key(0, ui.KeyEscape)
	_, cancel := context.WithCancel(t.Context())
	defer cancel()
	a.cancel = cancel
	view.Frame()
	view.RightClickAt(x(8), body)
	if a.position != afterClick {
		t.Fatalf("busy right-click moved playhead from %d to %d", afterClick, a.position)
	}
}

func TestContextMenusStayOffOverviewAndEmptyWorkspace(t *testing.T) {
	empty := &app{opts: filterOptions{preset: "中等"}}
	view := ui.NewTester(empty.view, 1180, 790)
	if err := view.RightClick("拖入音视频"); err != nil {
		t.Fatal(err)
	}
	if menu := view.Menu(); menu != nil {
		t.Fatalf("empty workspace menu = %q", menu)
	}

	item := &asset{info: media.Info{Duration: 30 * timeline.Second}}
	a := &app{assets: []*asset{item}, selected: item, opts: filterOptions{preset: "中等"}}
	view = ui.NewTester(a.view, 1180, 790)
	if err := view.RightClick("全局波形"); err != nil {
		t.Fatal(err)
	}
	if menu := view.Menu(); menu != nil {
		t.Fatalf("overview menu = %q", menu)
	}
}

func TestSelectionExportAsset(t *testing.T) {
	source := selectionExportFixture(t)
	item := asset{path: source, name: filepath.Base(source)}
	info, _, err := media.Waveform(context.Background(), source, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	item.info = info
	item.file, err = os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	a := &app{selected: &item}
	view := ui.NewTester(a.view, 1180, 790)
	wave, _ := view.Find("音频波形和播放位置")
	x := func(fraction float32) float32 { return wave.X + wave.W*fraction }
	y := wave.Y + wave.H/2
	view.Press(x(.5), y)
	view.Release(x(.625), y)
	view.Press(x(.625), y)
	view.Release(x(.75), y)
	span, ok := a.timelineSelection(&item)
	if !ok || span.Start != timeline.Second || span.End != timeline.Second+timeline.Second/2 {
		t.Fatalf("selection for export = %+v", span)
	}
	one, err := exportSelectionAsset(context.Background(), item, span, dir, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	two, err := exportSelectionAsset(context.Background(), item, span, dir, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(one) != "选区素材_选中区域.wav" || filepath.Base(two) != "选区素材_选中区域 (2).wav" {
		t.Fatalf("selection export names = %s, %s", one, two)
	}
	rendered, _, err := media.Waveform(context.Background(), one, func(float64) {})
	if err != nil || rendered.Duration < span.End-span.Start-1000 || rendered.Duration > span.End-span.Start+1000 {
		t.Fatalf("selection duration = %d, error %v", rendered.Duration, err)
	}
	decoder, err := media.OpenAudio(one)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	samples := make([]float32, 4096)
	n, err := decoder.Read(samples)
	if err != nil || n == 0 {
		t.Fatalf("read exported selection: %d samples, %v", n, err)
	}
	peak := float32(0)
	for _, sample := range samples[:n] {
		peak = max(peak, float32(math.Abs(float64(sample))))
	}
	if peak < .45 || peak > .7 {
		t.Fatalf("exported selection level = %.3f, want the second source interval", peak)
	}
	file, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte{0}); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := exportSelectionAsset(context.Background(), item, span, dir, func(float64) {}); err == nil ||
		!strings.Contains(err.Error(), "素材已改变") {
		t.Fatalf("changed source export error = %v", err)
	}
}

func selectionExportFixture(t *testing.T) string {
	t.Helper()
	const rate = 48000
	const length = rate * 2
	var wav bytes.Buffer
	wav.WriteString("RIFF")
	order := binary.LittleEndian
	_ = binary.Write(&wav, order, uint32(36+length*2))
	wav.WriteString("WAVEfmt ")
	_ = binary.Write(&wav, order, uint32(16))
	_ = binary.Write(&wav, order, uint16(1))
	_ = binary.Write(&wav, order, uint16(1))
	_ = binary.Write(&wav, order, uint32(rate))
	_ = binary.Write(&wav, order, uint32(rate*2))
	_ = binary.Write(&wav, order, uint16(2))
	_ = binary.Write(&wav, order, uint16(16))
	wav.WriteString("data")
	_ = binary.Write(&wav, order, uint32(length*2))
	for i := range length {
		amplitude := .2
		if i >= rate {
			amplitude = .6
		}
		sample := int16(amplitude * 32767 * math.Sin(float64(i)*2*math.Pi*440/rate))
		_ = binary.Write(&wav, order, sample)
	}
	path := filepath.Join(t.TempDir(), "选区素材.wav")
	if err := os.WriteFile(path, wav.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}
