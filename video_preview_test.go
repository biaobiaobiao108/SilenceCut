package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

func BenchmarkNativeVideoSeek(b *testing.B) {
	if os.Getenv("SILENCECUT_BENCH_PREVIEW") != "1" {
		b.Skip("set SILENCECUT_BENCH_PREVIEW=1 to measure the platform player")
	}
	source := filepath.Join(b.TempDir(), "long-gop.mp4")
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i",
		"color=red:s=640x360:r=30:d=6", "-f", "lavfi", "-i", "color=blue:s=640x360:r=30:d=6",
		"-filter_complex", "concat=n=2:v=1:a=0", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-g", "360", "-sc_threshold", "0", source).CombinedOutput()
	if err != nil {
		b.Fatalf("fixture: %v\n%s", err, out)
	}
	p, err := video.Open(source)
	if err != nil {
		b.Fatal(err)
	}
	defer p.Close()
	p.Pause()
	view := ui.NewTester(func(c *ui.Context) { ui.Video(c, p).Fill() }, 320, 180)
	hasFrame := func(blue bool) bool {
		view.Frame()
		if !p.Ready() {
			return false
		}
		px := view.Image().RGBAAt(160, 90)
		if blue {
			return px.B > 180 && px.R < 50
		}
		return px.R > 180 && px.B < 50
	}
	wait := func(b *testing.B, blue bool) {
		b.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !hasFrame(blue) {
			if err := p.Err(); err != nil {
				b.Fatal(err)
			}
			if time.Now().After(deadline) {
				b.Fatalf("timed out waiting for blue=%t, position=%v, ready=%t", blue, p.Position(), p.Ready())
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait(b, false)
	b.Run("paused", func(b *testing.B) {
		blue := true
		for b.Loop() {
			at := 2 * time.Second
			if blue {
				at = 8 * time.Second
			}
			p.Seek(at)
			wait(b, blue)
			blue = !blue
		}
	})
	b.Run("scrub-final-frame", func(b *testing.B) {
		item := &asset{info: media.Info{Duration: 12 * timeline.Second, Video: true}}
		a := &app{selected: item, previewState: previewState{player: p, video: p}}
		blue := true
		for b.Loop() {
			b.StopTimer()
			origin, target := 2, 8
			if !blue {
				origin, target = target, origin
			}
			p.Seek(time.Duration(origin) * time.Second)
			wait(b, !blue)
			a.position = int64(origin) * timeline.Second
			a.waveDrag = waveGesture{kind: wavePlayhead, end: a.position}
			for _, offset := range []int{1, 2, 3, 4, 5} {
				if !blue {
					offset += 6
				}
				a.updateWaveDrag(time.Now(), item, float32(offset*100), 1200, item.info.Duration, true)
				hasFrame(!blue)
				time.Sleep(seekInterval)
			}
			b.StartTimer()
			a.updateWaveDrag(time.Now(), item, float32(target*100), 1200, item.info.Duration, false)
			wait(b, blue)
			blue = !blue
		}
	})
	b.Run("playing-cut", func(b *testing.B) {
		item := &asset{
			info: media.Info{Duration: 12 * timeline.Second, Video: true},
			history: timeline.History{Clips: []timeline.Clip{
				{Span: timeline.Span{End: 5_750_000}, Keep: true},
				{Span: timeline.Span{Start: 5_750_000, End: 8_000_000}},
				{Span: timeline.Span{Start: 8_000_000, End: 12_000_000}, Keep: true},
			}},
		}
		a := &app{selected: item, previewState: previewState{player: p, video: p}}
		var latency time.Duration
		for b.Loop() {
			p.Pause()
			p.Seek(5500 * time.Millisecond)
			wait(b, false)
			a.position, a.videoSeekAt = 5_500_000, time.Time{}
			p.Play()
			deadline := time.Now().Add(3 * time.Second)
			for a.videoSeekAt.IsZero() {
				hasFrame(false)
				a.updatePlaybackPosition()
				if time.Now().After(deadline) {
					b.Fatalf("filtered playback did not reach the cut, position=%v", p.Position())
				}
				time.Sleep(time.Millisecond)
			}
			wait(b, true)
			latency += time.Since(a.videoSeekAt)
		}
		p.Pause()
		b.ReportMetric(float64(latency)/float64(b.N)/float64(time.Millisecond), "ms-to-cut-frame")
	})
}

type testPreviewPlayer struct {
	at        time.Duration
	playing   bool
	deferSeek bool
	seeks     []time.Duration
}

func (p *testPreviewPlayer) Position() time.Duration { return p.at }
func (p *testPreviewPlayer) Seek(at time.Duration) {
	p.seeks = append(p.seeks, at)
	if !p.deferSeek {
		p.at = at
	}
}
func (p *testPreviewPlayer) Playing() bool { return p.playing }
func (p *testPreviewPlayer) Play()         { p.playing = true }
func (p *testPreviewPlayer) Pause()        { p.playing = false }
func (p *testPreviewPlayer) Close()        { p.playing = false }

func TestVideoScrubCoalescesAndSeeksFinalPosition(t *testing.T) {
	item := &asset{info: media.Info{Duration: 10 * timeline.Second, Video: true}}
	p := &testPreviewPlayer{}
	a := &app{selected: item, previewState: previewState{player: p}, timelineState: timelineState{waveDrag: waveGesture{kind: wavePlayhead}}}
	start := time.Now()
	a.updateWaveDrag(start, item, 100, 1000, item.info.Duration, true)
	a.updateWaveDrag(start.Add(40*time.Millisecond), item, 200, 1000, item.info.Duration, true)
	if a.position != 2*timeline.Second || len(p.seeks) != 1 {
		t.Fatalf("intermediate drag: position %d, seeks %v", a.position, p.seeks)
	}
	a.updateWaveDrag(start.Add(110*time.Millisecond), item, 300, 1000, item.info.Duration, true)
	a.updateWaveDrag(start.Add(120*time.Millisecond), item, 400, 1000, item.info.Duration, false)
	if len(p.seeks) != 3 || p.seeks[1] != 3*time.Second || p.seeks[2] != 4*time.Second {
		t.Fatalf("latest and final seeks: %v", p.seeks)
	}
}

func TestFilteredVideoSkipsRemovedRangesInSource(t *testing.T) {
	s := timeline.Second
	item := &asset{info: media.Info{Duration: 9 * s, Video: true}, history: timeline.History{Clips: []timeline.Clip{
		{Span: timeline.Span{End: s}},
		{Span: timeline.Span{Start: s, End: 3 * s}, Keep: true},
		{Span: timeline.Span{Start: 3 * s, End: 5 * s}},
		{Span: timeline.Span{Start: 5 * s, End: 7 * s}, Keep: true},
		{Span: timeline.Span{Start: 7 * s, End: 9 * s}},
	}}}
	p := &testPreviewPlayer{}
	a := &app{selected: item, previewState: previewState{player: p}}
	a.togglePlay()
	if !p.Playing() || p.at != time.Second || a.position != s {
		t.Fatalf("expected playback to start after leading silence, cursor %d, player %s", a.position, p.at)
	}
	a.updatePlaybackPosition()
	p.at = 2999 * time.Millisecond
	if !a.updatePlaybackPosition() || a.position != 2_999_000 || len(p.seeks) != 1 {
		t.Fatalf("kept video should advance without a seek, cursor %d, seeks %v", a.position, p.seeks)
	}

	p.at, p.deferSeek = 3*time.Second, true
	if !a.updatePlaybackPosition() || a.position != 5*s || p.seeks[1] != 5*time.Second {
		t.Fatalf("expected a source seek from 3s to 5s, cursor %d, seeks %v", a.position, p.seeks)
	}
	if !a.updatePlaybackPosition() || a.position != 5*s || len(p.seeks) != 2 {
		t.Fatalf("pending native seek regressed or retried, cursor %d, seeks %v", a.position, p.seeks)
	}
	p.at, p.deferSeek = 5*time.Second, false
	a.updatePlaybackPosition()
	p.at = 5500 * time.Millisecond
	a.togglePlay()
	a.togglePlay()
	if !p.Playing() || a.position != 5_500_000 || len(p.seeks) != 2 {
		t.Fatalf("pause and resume should retain the source position, cursor %d, seeks %v", a.position, p.seeks)
	}
	p.at = 7015 * time.Millisecond
	if a.updatePlaybackPosition() || a.position != 7*s || p.Playing() || p.at != 7*time.Second {
		t.Fatalf("expected playback to stop before the removed tail, cursor %d, player %s", a.position, p.at)
	}
	p.deferSeek = true
	a.seek(2 * s)
	a.togglePlay()
	if !a.updatePlaybackPosition() || a.position != 2*s || len(p.seeks) != 4 {
		t.Fatalf("stale timestamp after seeking backwards, cursor %d, seeks %v", a.position, p.seeks)
	}
	p.at, p.deferSeek = 2*time.Second, false
	a.updatePlaybackPosition()
	p.at = 2100 * time.Millisecond
	if !a.updatePlaybackPosition() || a.position != 2_100_000 {
		t.Fatalf("playhead did not resume after seeking backwards, cursor %d", a.position)
	}
}

func TestOriginalVideoUsesSourceClock(t *testing.T) {
	s := timeline.Second
	item := &asset{
		info: media.Info{Duration: 9 * s, Video: true},
		history: timeline.History{Clips: []timeline.Clip{
			{Span: timeline.Span{End: s}, Keep: true},
			{Span: timeline.Span{Start: s, End: 9 * s}},
		}},
	}
	p := &testPreviewPlayer{}
	a := &app{selected: item, previewState: previewState{player: p}}
	a.setPreviewMode(true)
	a.togglePlay()
	p.at = 3 * time.Second
	if !a.updatePlaybackPosition() || a.position != 3*s {
		t.Fatalf("original playback should play removed intervals, got %d", a.position)
	}
}

func TestFilteredVideoAnticipatesCutBetweenUpdates(t *testing.T) {
	item := &asset{info: media.Info{Duration: 2 * timeline.Second, Video: true}, history: timeline.History{Clips: []timeline.Clip{
		{Span: timeline.Span{End: timeline.Second}, Keep: true},
		{Span: timeline.Span{Start: timeline.Second, End: 1_005_000}},
		{Span: timeline.Span{Start: 1_005_000, End: 2 * timeline.Second}, Keep: true},
	}}}
	p := &testPreviewPlayer{at: 995 * time.Millisecond, playing: true}
	a := &app{selected: item, previewState: previewState{player: p}}
	if !a.updatePlaybackPosition() || len(p.seeks) != 1 || p.seeks[0] != 1005*time.Millisecond {
		t.Fatalf("expected a seek before the short cut passes between frames, got %v", p.seeks)
	}
}
