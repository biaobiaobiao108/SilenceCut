package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

func TestPlayPromotesTheExistingFirstChunk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	item := &asset{path: path, file: file, info: media.Info{Duration: 3 * timeline.Second, Video: true},
		history: timeline.History{Clips: []timeline.Clip{
			{Span: timeline.Span{End: 2 * timeline.Second}, Keep: true},
			{Span: timeline.Span{Start: 2 * timeline.Second, End: 3 * timeline.Second}},
		}}}
	canceled := false
	job := &previewJob{cancel: func() { canceled = true }, cut: previewChunk(item.history.Clips, 0)}
	a := &app{selected: item, player: &testPreviewPlayer{}, chunkJob: job, previewProgress: .5}
	a.togglePlay()
	if canceled || a.chunkJob != job || !a.buildingCut || a.previewProgress != .5 {
		t.Fatal("play should promote the in-flight chunk without restarting the encode")
	}
	item.file = nil
	a.togglePlay()
	if !canceled || a.chunkJob != nil || a.buildingCut {
		t.Fatal("the same play control should cancel a pending audition")
	}
}

func TestPreviewChunksReuseUnaffectedSourceWindows(t *testing.T) {
	s := timeline.Second
	h := timeline.History{Clips: []timeline.Clip{
		{Span: timeline.Span{End: 3 * s}, Keep: true},
		{Span: timeline.Span{Start: 3 * s, End: 10 * s}},
		{Span: timeline.Span{Start: 10 * s, End: 14 * s}, Keep: true},
		{Span: timeline.Span{Start: 14 * s, End: 26 * s}},
		{Span: timeline.Span{Start: 26 * s, End: 32 * s}, Keep: true},
	}}
	first := previewChunk(h.Clips, 0)
	second := previewChunk(h.Clips, 12*s)
	third := previewChunk(h.Clips, 26*s)
	if !slices.Equal(first.spans, []timeline.Span{{End: 3 * s}, {Start: 10 * s, End: 12 * s}}) ||
		second.sourcePosition(0) != first.sourcePosition(5*s) {
		t.Fatalf("retained video does not meet at the window boundary: %v %v", first.spans, second.spans)
	}
	if at, ok := nextRetained(h.Clips, second.windowEnd); !ok || at != 26*s {
		t.Fatalf("next chunk did not skip the removed interval: %d, %v", at, ok)
	}
	h.Edit(timeline.Span{Start: s, End: 2 * s}, false)
	if first.matches(previewChunk(h.Clips, 0)) || !second.matches(previewChunk(h.Clips, 12*s)) ||
		!third.matches(previewChunk(h.Clips, 26*s)) {
		t.Fatal("editing one interval should invalidate only its source window")
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
