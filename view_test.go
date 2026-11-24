package main

import (
	"context"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
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

func TestWorkspaceLayoutAndEdits(t *testing.T) {
	for _, size := range [][2]int{{930, 690}, {1120, 790}, {1180, 790}, {1600, 900}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			a := &app{opts: filterOptions{preset: "慢"}}
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
				info: media.Info{Duration: 5 * timeline.Second, Rate: 48000, Channels: 1},
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
			for _, label := range []string{"打开项目", "保存项目", "导出切片", "导出合并媒体", settings, "音频波形和播放位置", "移除", "撤销"} {
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
			for _, label := range []string{"静音阈值 (dB)", "片段后留白 (秒)", "重新过滤", "试听模式", "放大波形"} {
				r, ok := view.Find(label)
				if !ok || r.W <= 0 || r.H <= 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
					t.Fatalf("control %q is not visible at 200%% DPI: %+v", label, r)
				}
			}
			if size[0] < wideWorkspace {
				view.Key(0, ui.KeyEscape)
				if _, open := view.Find("重新过滤"); open || !view.Focused("过滤设置") {
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
			for _, label := range []string{"打开项目", "保存项目", "导出切片", "导出合并媒体"} {
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
	project, ok := view.Find(filepath.Base(projectPath))
	if !ok || project.X+project.W > sideWidth {
		t.Fatalf("project name crosses the material rail: %+v", project)
	}
	for _, label := range []string{"打开项目", "保存项目"} {
		r, ok := view.Find(label)
		if !ok || r.X < sideWidth+1 {
			t.Fatalf("%s is not fully inside the editor column: %+v", label, r)
		}
	}
	for _, label := range []string{"导出切片", "导出合并媒体"} {
		r, ok := view.Find(label)
		if !ok || r.X+r.W > width-settingsWidth-1 {
			t.Fatalf("%s crosses the inspector divider: %+v", label, r)
		}
	}
}

func TestFocusModeGivesTheWaveformRoom(t *testing.T) {
	item := &asset{name: "口播.wav", info: media.Info{Duration: 5 * timeline.Second}}
	a := &app{assets: []*asset{item}, selected: item, opts: filterOptions{preset: "慢"}}
	view := ui.NewTester(a.view, 1180, 790)
	wave, _ := view.Find("音频波形和播放位置")
	if err := view.Click("专注模式"); err != nil {
		t.Fatal(err)
	}
	view.Advance(revealDuration + 20*time.Millisecond)
	focused, _ := view.Find("音频波形和播放位置")
	if focused.W <= wave.W+400 {
		t.Fatalf("focus mode did not widen the waveform: before %.1f after %.1f", wave.W, focused.W)
	}
	if view.HasText("素材") {
		t.Fatal("material rail remained visible in focus mode")
	}
	if view.HasText("仅修剪首尾") {
		t.Fatal("filter inspector remained visible in focus mode")
	}
	if folder := os.Getenv("SILENCECUT_TEST_CAPTURE"); folder != "" {
		saveView(t, view, filepath.Join(folder, "focus-1180.png"))
	}
	if err := view.Click("专注模式"); err != nil {
		t.Fatal(err)
	}
	view.Advance(revealDuration + 20*time.Millisecond)
	restored, _ := view.Find("音频波形和播放位置")
	if math.Abs(float64(restored.W-wave.W)) > 2 {
		t.Fatalf("waveform did not return after focus mode: before %.1f after %.1f", wave.W, restored.W)
	}

	reduced := &app{assets: []*asset{item}, selected: item, opts: filterOptions{preset: "慢"}}
	reducedView := ui.NewTesterWith(reduced.view, 1180, 790, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	before, _ := reducedView.Find("音频波形和播放位置")
	if err := reducedView.Click("专注模式"); err != nil {
		t.Fatal(err)
	}
	reducedView.Frame()
	after, _ := reducedView.Find("音频波形和播放位置")
	if after.W <= before.W+400 || reducedView.HasText("素材") {
		t.Fatalf("reduced motion blocked focus mode: before %.1f after %.1f", before.W, after.W)
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
	if !near(a.selection.Start, 16*second) || !near(a.selection.End, 17*second) {
		t.Fatalf("blank area did not select: %v", a.selection)
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
	click("重新过滤")
	if !view.HasText("请输入 0 到 10 秒") || view.HasText("请输入 −70 到 20 dB") {
		t.Fatal("expected only the invalid field's message")
	}
	selection := a.selection
	view.SetSize(wideWorkspace, 690)
	if _, ok := view.Find("重新过滤"); !ok || a.settingsOpen || a.opts.before != "错" ||
		a.position != 700_000 || a.selection != selection || a.viewLength != 2*timeline.Second {
		t.Fatal("crossing the breakpoint changed the draft or editor state")
	}
	view.SetSize(wideWorkspace-1, 690)
	if _, ok := view.Find("重新过滤"); ok {
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
	a.end(batchStatus("导出", "已导出 0 个文件", 0, 1, "详情见素材列表", false))
	view.Frame()
	if !view.HasText("导出失败 · 已导出 0 个文件 · 1 个素材失败 · 详情见素材列表") ||
		!view.HasText("导出失败 · 文件丢失") || view.HasText("打开导出文件夹") {
		t.Fatal("failed export did not show the failure or exposed the folder prematurely")
	}
	a.publishedDir = a.outputDir
	item.exportIssue = ""
	a.status = batchStatus("导出", "已导出 1 个文件", 1, 1, "详情见素材列表", false)
	view.Frame()
	if !view.HasText("导出部分成功 · 已导出 1 个文件 · 1 个素材失败 · 详情见素材列表") || !view.HasText("打开导出文件夹") {
		t.Fatal("partial export did not expose the result status and folder")
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
