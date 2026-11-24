package main

import (
	"testing"
	"time"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

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
	a := &app{selected: item, player: p}
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
	a := &app{selected: item, player: p}
	a.setPreviewMode(true)
	a.togglePlay()
	p.at = 3 * time.Second
	if !a.updatePlaybackPosition() || a.position != 3*s {
		t.Fatalf("original playback should play removed intervals, got %d", a.position)
	}
}
