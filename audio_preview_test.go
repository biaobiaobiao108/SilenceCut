package main

import (
	"io"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

type sampleSource struct {
	samples []float32
	at      int
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

type testPreviewPlayer struct {
	at      time.Duration
	playing bool
}

func (p *testPreviewPlayer) Position() time.Duration { return p.at }
func (p *testPreviewPlayer) Seek(at time.Duration)   { p.at = at }
func (p *testPreviewPlayer) Playing() bool           { return p.playing }
func (p *testPreviewPlayer) Play()                   { p.playing = true }
func (p *testPreviewPlayer) Pause()                  { p.playing = false }
func (p *testPreviewPlayer) Close()                  { p.playing = false }

func TestPreviewModeStartsFilteredAndCanPlayOriginal(t *testing.T) {
	s := timeline.Second
	item := &asset{
		info: media.Info{Duration: 9 * s, Video: true},
		history: timeline.History{Clips: []timeline.Clip{
			{Span: timeline.Span{End: s}},
			{Span: timeline.Span{Start: s, End: 3 * s}, Keep: true},
			{Span: timeline.Span{Start: 3 * s, End: 5 * s}},
			{Span: timeline.Span{Start: 5 * s, End: 7 * s}, Keep: true},
			{Span: timeline.Span{Start: 7 * s, End: 9 * s}},
		}},
	}
	p := &testPreviewPlayer{}
	a := &app{selected: item, player: p, video: &ui.VideoPlayer{}}
	a.togglePlay()
	if !p.playing || p.at != time.Second {
		t.Fatalf("play should skip leading cut, got %t at %s", p.playing, p.at)
	}
	p.at = 3 * time.Second
	if !a.updatePlaybackPosition() || p.at != 5*time.Second {
		t.Fatalf("play should skip middle cut, got %t at %s", p.playing, p.at)
	}
	p.at = 7 * time.Second
	if a.updatePlaybackPosition() || p.playing || a.position != 7*s {
		t.Fatalf("play should stop at the last retained boundary, got %t at %d", p.playing, a.position)
	}
	a.setPreviewMode(true)
	a.seek(0)
	a.togglePlay()
	p.at = 3 * time.Second
	if !a.updatePlaybackPosition() || a.position != 3*s {
		t.Fatalf("original should retain the source cursor, got %t at %d", p.playing, a.position)
	}
	a.setPreviewMode(false)
	if !p.playing || p.at != 5*time.Second {
		t.Fatalf("switching back to filtered should skip the active cut, got %t at %s", p.playing, p.at)
	}
}
