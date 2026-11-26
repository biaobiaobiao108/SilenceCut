package main

import (
	"context"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

type sampleSource struct {
	samples []float32
	at      int
}

func TestAudioFormatWorkflow(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is used to generate audio fixtures")
	}
	for _, tc := range []struct{ extension, encoder string }{
		{"wav", "pcm_s16le"}, {"opus", "libopus"}, {"aiff", "pcm_s16be"},
		{"caf", "pcm_s16le"}, {"wma", "wmav2"}, {"ac3", "ac3"},
	} {
		t.Run(tc.extension, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "素材."+tc.extension)
			out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i",
				"aevalsrc='0.2*sin(2*PI*440*t)*(between(t,0.5,2)+between(t,5,7)+between(t,9,11))':s=48000:d=12",
				"-c:a", tc.encoder, source).CombinedOutput()
			if err != nil {
				t.Fatalf("fixture: %v\n%s", err, out)
			}
			ctx := context.Background()
			progress := func(float64) {}
			_, peaks, err := media.Waveform(ctx, source, progress)
			if err != nil {
				t.Fatal(err)
			}
			clips, err := media.Detect(ctx, source, timeline.Settings{Threshold: timeline.AutomaticThreshold(peaks)}, progress)
			if err != nil {
				t.Fatal(err)
			}
			kept := timeline.Kept(clips)
			if len(kept) != 3 {
				t.Fatalf("retained %v", kept)
			}
			for _, mode := range [][]timeline.Clip{nil, clips} {
				p, err := openAudioPreview(source, mode)
				if err != nil {
					t.Fatal(err)
				}
				p.Close()
			}
			original, err := media.OpenAudio(source)
			if err != nil {
				t.Fatal(err)
			}
			defer original.Close()
			assertLevel := func(d interface {
				Read([]float32) (int, error)
				SetPosition(int64) error
			}, frame int64, loud bool) {
				t.Helper()
				if err := d.SetPosition(frame); err != nil {
					t.Fatal(err)
				}
				buf := make([]float32, 4096)
				n, err := d.Read(buf)
				var peak float64
				for _, sample := range buf[:n] {
					peak = max(peak, math.Abs(float64(sample)))
				}
				if err != nil || n != len(buf) || loud && peak < .1 || !loud && peak > .01 {
					t.Fatalf("frame %d, loud=%t: peak %.3f, %d samples, error %v", frame, loud, peak, n, err)
				}
			}
			assertLevel(original, 48000, true)
			assertLevel(original, 3*48000, false)
			assertLevel(original, 10*48000, true)
			assertLevel(original, 48000, true)
			cut := newAuditionDecoder(original, nil, clips)
			boundary := cut.spans[2].editedStart
			assertLevel(cut, boundary+24000, true)
			rendered, err := media.Export(ctx, source, dir, "导出", kept, progress)
			if err != nil {
				t.Fatal(err)
			}
			info, _, err := media.Waveform(ctx, rendered, progress)
			// The exporter rounds cumulative time, so independent source cuts
			// can differ by up to one sample per retained interval.
			if err != nil || math.Abs(float64(info.Duration-cut.Len()*timeline.Second/48000)) > float64(len(kept))*22 {
				t.Fatalf("exported %d µs, preview %d samples, error %v", info.Duration, cut.Len(), err)
			}
		})
	}
}

func (s *sampleSource) SampleRate() int { return 1000 }
func (s *sampleSource) Channels() int   { return 1 }
func (s *sampleSource) Len() int64      { return int64(len(s.samples)) }
func (s *sampleSource) SetPosition(at int64) error {
	s.at = int(at)
	return nil
}
func (s *sampleSource) Read(buf []float32) (int, error) {
	n := copy(buf, s.samples[s.at:])
	s.at += n
	if s.at == len(s.samples) {
		return n, io.EOF
	}
	return n, nil
}

func TestAuditionReadsOnlyRetainedSamples(t *testing.T) {
	// The loud samples stand in for a manually removed sound, including at both ends.
	src := &sampleSource{samples: []float32{1, .1, .2, 1, 1, .3, .4, 1}}
	clips := []timeline.Clip{
		{Span: timeline.Span{Start: 1000, End: 3000}, Keep: true},
		{Span: timeline.Span{Start: 5000, End: 7000}, Keep: true},
	}
	d := newAuditionDecoder(src, nil, clips)
	buf := make([]float32, 10)
	n, err := d.Read(buf)
	if err != nil || d.Len() != 4 || !slices.Equal(buf[:n], []float32{.1, .2, .3, .4}) {
		t.Fatalf("rendered samples = %v, length %d, error %v", buf[:n], d.Len(), err)
	}
	if n, err = d.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("read after retained end returned %d, %v", n, err)
	}
	if err := d.SetPosition(2); err != nil {
		t.Fatal(err)
	}
	n, err = d.Read(buf)
	if err != nil || !slices.Equal(buf[:n], []float32{.3, .4}) {
		t.Fatalf("seek across removed region = %v, error %v", buf[:n], err)
	}
}

func TestPreviewDownmixesCenterChannel(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is used to generate a surround fixture")
	}
	source := filepath.Join(t.TempDir(), "center.flac")
	out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i",
		"aevalsrc='0|0|0.4*sin(2*PI*440*t)|0|0|0':s=48000:d=1:c=5.1", source).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	decoder, err := media.OpenAudio(source)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	if decoder.Channels() != 2 {
		t.Fatalf("preview has %d channels", decoder.Channels())
	}
	var pcm [2048]float32
	n, err := decoder.Read(pcm[:])
	if err != nil || n != len(pcm) {
		t.Fatalf("read %d samples: %v", n, err)
	}
	var left, right float32
	for i := 0; i < n; i += 2 {
		left, right = max(left, pcm[i]), max(right, pcm[i+1])
	}
	if left < .1 || right < .1 {
		t.Fatalf("center channel lost in stereo: left %.3f right %.3f", left, right)
	}
}

func TestOpenPreviewMatchesPreviewMode(t *testing.T) {
	source := selectionExportFixture(t)
	clips := []timeline.Clip{
		{Span: timeline.Span{Start: 0, End: timeline.Second}, Keep: true},
		{Span: timeline.Span{Start: timeline.Second, End: 2 * timeline.Second}, Keep: false},
	}
	item := &asset{
		path:    source,
		info:    media.Info{Duration: 2 * timeline.Second, Rate: 48000, Channels: 1},
		history: timeline.History{Clips: clips},
	}

	a := &app{selected: item}
	a.openPreview(item)
	if a.player == nil {
		t.Fatalf("expected preview player, got error %q", a.previewIssue)
	}
	if _, ok := a.player.(*auditionPlayer); !ok {
		a.closePreview()
		t.Fatalf("filtered mode should open auditionPlayer directly, got %T", a.player)
	}
	if !a.prepareAudioPlayback(clips) {
		a.closePreview()
		t.Fatal("prepareAudioPlayback failed")
	}
	a.closePreview()

	a.originalPreview = true
	a.openPreview(item)
	if a.player == nil {
		t.Fatalf("expected original preview player, got error %q", a.previewIssue)
	}
	if _, ok := a.player.(*audioPreview); !ok {
		a.closePreview()
		t.Fatalf("original mode should open audioPreview directly, got %T", a.player)
	}
	a.closePreview()
}
