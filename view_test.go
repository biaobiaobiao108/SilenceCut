package main

import (
	"context"
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

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
	level := timeline.AutomaticThreshold(peaks, info.Rate)
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
	for _, size := range [][2]int{{930, 690}, {1180, 790}, {1600, 900}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			a := &app{opts: filterOptions{preset: "慢"}}
			view := ui.NewTester(a.view, size[0], size[1])
			if !view.HasText("拖入文件") {
				t.Fatal("empty workspace lacks its drop target")
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
			for _, label := range []string{"导出切片", "导出合并媒体", "重新过滤", "音频波形和播放位置", "移除", "撤销"} {
				r, ok := view.Find(label)
				if !ok || r.X < 0 || r.Y < 0 || r.W <= 0 || r.H <= 0 ||
					r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
					t.Fatalf("control %q is not fully visible: %+v", label, r)
				}
			}
			if folder := os.Getenv("SILENCECUT_TEST_CAPTURE"); folder != "" {
				path := filepath.Join(folder, fmt.Sprintf("workspace-%d.png", size[0]))
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
			if err := view.Click("自定义"); err != nil {
				t.Fatal(err)
			}
			for _, label := range []string{"静音阈值 (dB)", "片段后留白 (秒)", "重新过滤", "试听模式", "放大波形"} {
				r, ok := view.Find(label)
				if !ok || r.W <= 0 || r.H <= 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
					t.Fatalf("control %q is not visible at 200%% DPI: %+v", label, r)
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
