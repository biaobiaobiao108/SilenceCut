package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

// A filtered video contains only retained media. The editor still uses source time.
type filteredVideo struct {
	path      string
	spans     []timeline.Span
	windowEnd int64
}

func (v *filteredVideo) editedPosition(source int64) int64 {
	var edited int64
	for _, span := range v.spans {
		if source < span.End {
			return edited + max(0, source-span.Start)
		}
		edited += span.End - span.Start
	}
	return edited
}

func (v *filteredVideo) sourcePosition(edited int64) int64 {
	for _, span := range v.spans {
		length := span.End - span.Start
		if edited < length {
			return span.Start + max(0, edited)
		}
		edited -= length
	}
	return v.spans[len(v.spans)-1].End
}

const (
	previewWindow    = 12 * timeline.Second
	previewEdge      = 960
	previewCacheSize = 12
)

type previewJob struct {
	cancel context.CancelFunc
	cut    *filteredVideo
}

func (a *app) needsFilteredVideo() bool {
	if a.originalPreview || a.selected == nil || !a.selected.info.Video {
		return false
	}
	clips := a.selected.history.Clips
	return a.selected.nativePreviewFailed && a.selected.proxy == "" ||
		len(clips) != 1 || clips[0].Span != (timeline.Span{End: a.selected.info.Duration})
}

// Use stable source-time windows so an edit affects only the intersecting files.
func previewChunk(clips []timeline.Clip, at int64) *filteredVideo {
	start := at / previewWindow * previewWindow
	cut := &filteredVideo{windowEnd: start + previewWindow}
	for _, clip := range clips[clipAt(clips, start):] {
		if clip.Start >= cut.windowEnd {
			break
		}
		if clip.Keep {
			cut.spans = append(cut.spans, timeline.Span{Start: max(start, clip.Start), End: min(cut.windowEnd, clip.End)})
		}
	}
	return cut
}

func (v *filteredVideo) matches(other *filteredVideo) bool {
	return v != nil && other != nil && v.windowEnd == other.windowEnd && slices.Equal(v.spans, other.spans)
}

func clearAssetPreviews(item *asset) {
	item.nativePreviewFailed = false
	if item.proxy != "" {
		_ = os.Remove(item.proxy)
		item.proxy = ""
	}
	for _, cut := range item.cutPreviews {
		_ = os.Remove(cut.path)
	}
	item.cutPreviews = nil
}

func (a *app) cancelChunk() {
	if a.chunkJob != nil {
		a.chunkJob.cancel()
		a.chunkJob = nil
	}
	if a.nextVideo != nil {
		a.nextVideo.Close()
	}
	a.nextVideo, a.nextCut = nil, nil
	a.buildingCut, a.previewProgress = false, 0
}

func (a *app) scheduleChunkWarm() {
	if a.win != nil && a.needsFilteredVideo() {
		a.warmAt = time.Now().Add(180 * time.Millisecond)
	}
}

func (a *app) warmChunkTick(c *ui.Context) {
	if a.warmAt.IsZero() {
		return
	}
	if delay := time.Until(a.warmAt); delay > 0 {
		c.After(delay)
		return
	}
	a.warmAt = time.Time{}
	if !a.busy() && a.waveDrag.kind == waveIdle && !a.overviewDrag.active {
		a.primeFilteredVideo()
	}
}

func (a *app) primeFilteredVideo() {
	item := a.selected
	if !a.needsFilteredVideo() || item.offline || item.file == nil || a.videoCut != nil || a.buildingCut || a.previewPlay {
		return
	}
	at, ok := nextRetained(item.history.Clips, a.position)
	if !ok {
		at, ok = nextRetained(item.history.Clips, 0)
	}
	if ok {
		a.requestChunk(at, false)
	}
}

func (a *app) requestChunk(at int64, play bool) {
	item := a.selected
	cut := previewChunk(item.history.Clips, at)
	if len(cut.spans) == 0 {
		return
	}
	if play && a.videoCut == nil {
		a.closePreviewPlayer()
	}
	file, err := os.Stat(item.path)
	if err == nil && changed(item.file, file) {
		err = errors.New("素材已改变，请先重新过滤")
	}
	if err != nil {
		if play {
			a.filteredVideoFailed(err.Error())
		}
		return
	}
	if play && a.videoCut.matches(cut) {
		a.startPlayback()
		return
	}
	for i := len(item.cutPreviews) - 1; i >= 0; i-- {
		if cached := item.cutPreviews[i]; cached.matches(cut) {
			item.cutPreviews = append(slices.Delete(item.cutPreviews, i, i+1), cached)
			a.useChunk(cached, play)
			return
		}
	}
	if a.chunkJob != nil && a.chunkJob.cut.matches(cut) {
		a.buildingCut = play
		return
	}
	a.cancelChunk()
	ctx, cancel := context.WithCancel(context.Background())
	job := &previewJob{cancel: cancel, cut: cut}
	a.chunkJob, a.buildingCut = job, play
	source, dir := item.path, a.temp
	a.async(func() {
		var updated time.Time
		path, err := media.Preview(ctx, source, dir, "filtered-preview", cut.spans, previewEdge, func(value float64) {
			if value == 1 || time.Since(updated) >= 100*time.Millisecond {
				updated = time.Now()
				a.win.Update(func() {
					if a.chunkJob == job {
						a.previewProgress = value
					}
				})
			}
		})
		a.win.Update(func() {
			if a.closing || a.chunkJob != job {
				if path != "" {
					_ = os.Remove(path)
				}
				return
			}
			play := a.buildingCut
			cancel()
			a.chunkJob, a.buildingCut, a.previewProgress = nil, false, 0
			if err != nil {
				if play {
					a.filteredVideoFailed(err.Error())
				} else if a.video == nil {
					a.previewIssue = err.Error()
				}
				return
			}
			cut.path = path
			item.cutPreviews = append(item.cutPreviews, cut)
			if len(item.cutPreviews) > previewCacheSize {
				for i, old := range item.cutPreviews {
					if old != a.videoCut {
						_ = os.Remove(old.path)
						item.cutPreviews = slices.Delete(item.cutPreviews, i, i+1)
						break
					}
				}
			}
			a.useChunk(cut, play)
		})
	})
}

func (a *app) useChunk(cut *filteredVideo, play bool) {
	if !a.nextCut.matches(cut) {
		a.cancelChunk()
		p, err := video.Open(cut.path)
		if err != nil {
			a.dropChunk(cut)
			if play {
				a.filteredVideoFailed(err.Error())
			} else if a.video == nil {
				a.previewIssue = err.Error()
			}
			return
		}
		p.Pause()
		a.nextVideo, a.nextCut = p, cut
	}
	if !play {
		return
	}
	if a.player != nil {
		a.player.Close()
	}
	a.previewSeq++
	a.player, a.video = a.nextVideo, a.nextVideo
	a.nextVideo, a.nextCut = nil, nil
	a.videoCut, a.previewSince = cut, time.Now()
	a.previewIssue, a.previewPlay, a.wasPlaying = "", true, false
}

func (a *app) prefetchChunk() {
	if a.videoCut != nil && a.videoCut.windowEnd > 0 {
		if next, ok := nextRetained(a.selected.history.Clips, a.videoCut.windowEnd); ok {
			a.requestChunk(next, false)
		}
	}
}

func (a *app) dropChunk(cut *filteredVideo) {
	if cut != nil {
		item := a.selected
		item.cutPreviews = slices.DeleteFunc(item.cutPreviews, func(v *filteredVideo) bool { return v == cut })
		_ = os.Remove(cut.path)
	}
}

func (a *app) filteredVideoFailed(issue string) {
	cut := a.videoCut
	a.closePreview()
	a.dropChunk(cut)
	a.openPreview(a.selected, false)
	if a.video == nil {
		a.previewIssue = issue
	}
	a.status = "无法播放过滤后试听 · " + issue
}
