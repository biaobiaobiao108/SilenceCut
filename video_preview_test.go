package main

import (
	"testing"
	"time"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

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

func TestFilteredVideoUsesEditedClock(t *testing.T) {
	s := timeline.Second
	cut := &filteredVideo{spans: []timeline.Span{
		{Start: s, End: 3 * s},
		{Start: 5 * s, End: 7 * s},
	}}
	item := &asset{info: media.Info{Duration: 9 * s, Video: true}}
	p := &testPreviewPlayer{}
	a := &app{selected: item, player: p, videoCut: cut}
	if a.originalPreview {
		t.Fatal("preview should default to filtered mode")
	}

	a.seek(5*s + 200_000)
	a.startPlayback()
	if p.at != 2200*time.Millisecond {
		t.Fatalf("seeking in source time moved the edited player to %s", p.at)
	}
	p.at = 1999 * time.Millisecond
	if !a.updatePlaybackPosition() || a.position != 2_999_000 {
		t.Fatalf("unexpected source position before the cut: %d", a.position)
	}
	p.at = 2 * time.Second
	if !a.updatePlaybackPosition() || a.position != 5*s || p.at != 2*time.Second {
		t.Fatalf("edited playback should cross the cut without seeking, cursor %d, player %s", a.position, p.at)
	}
	p.at, p.playing = 4*time.Second, false
	if a.updatePlaybackPosition() || a.position != 7*s {
		t.Fatalf("playback should finish at the last retained boundary, got %d", a.position)
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
	a := &app{selected: item, player: p}
	a.setPreviewMode(true)
	a.togglePlay()
	p.at = 3 * time.Second
	if !a.updatePlaybackPosition() || a.position != 3*s {
		t.Fatalf("original playback should play removed intervals, got %d", a.position)
	}
}
