package main

import (
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"silencecut/internal/timeline"
)

type previewState struct {
	previewSeq                int
	originalPreview           bool
	player                    player
	video                     *ui.VideoPlayer
	previewIssue              string
	previewSince, videoSeekAt time.Time
	wasPlaying                bool
}

func (a *app) closePreview() {
	a.previewSeq++
	if a.player != nil {
		a.player.Close()
	}
	a.player, a.video, a.previewIssue = nil, nil, ""
	a.previewSince, a.videoSeekAt = time.Time{}, time.Time{}
	a.wasPlaying = false
}

func (a *app) openPreview(item *asset) {
	if item.offline {
		return
	}
	if item.info.Video {
		p, err := video.Open(item.path)
		if err != nil {
			a.previewIssue = err.Error()
			return
		}
		p.Pause()
		a.player, a.video, a.previewSince = p, p, time.Now()
		return
	}
	var clips []timeline.Clip
	if !a.originalPreview {
		clips = item.history.Clips
	}
	p, err := openAudioPreview(item.path, clips)
	if err != nil {
		a.previewIssue = err.Error()
		return
	}
	a.player = p
	a.seek(a.position)
}

func (a *app) seek(position int64) {
	if a.selected == nil {
		return
	}
	a.position = max(0, min(position, a.selected.info.Duration))
	a.scheduleRecovery()
	if a.player == nil || a.video != nil && !a.video.Ready() {
		return
	}
	if a.selected.info.Video {
		a.videoSeekAt = time.Now()
	}
	a.player.Seek(time.Duration(a.position) * time.Microsecond)
}

func (a *app) togglePlay() {
	if a.busy() || a.waveDrag.kind != waveIdle || a.overviewDrag.active || a.selected == nil || a.selected.offline ||
		a.player == nil || a.video != nil && !a.video.Ready() {
		return
	}
	if a.player.Playing() {
		a.player.Pause()
		a.updatePlaybackPosition()
		return
	}
	clips := a.selected.history.Clips
	target := a.position
	if !a.originalPreview {
		var ok bool
		if target, ok = nextRetained(clips, target); !ok {
			if target, ok = nextRetained(clips, 0); !ok {
				return
			}
		}
	} else if target >= a.selected.info.Duration {
		target = 0
	}
	if !a.selected.info.Video && !a.prepareAudioPlayback(clips) {
		return
	}
	if a.video != nil && a.video.Ended() ||
		target != a.player.Position().Microseconds() && (target != a.position || a.videoSeekAt.IsZero()) {
		a.seek(target)
	}
	a.position = target
	a.followPlayhead()
	a.player.Play()
	a.wasPlaying = a.player.Playing()
}

func (a *app) prepareAudioPlayback(clips []timeline.Clip) bool {
	cut, edited := a.player.(*auditionPlayer)
	if a.originalPreview && !edited || !a.originalPreview && edited && slices.Equal(cut.clips, clips) {
		return true
	}
	if a.originalPreview {
		clips = nil
	}
	p, err := openAudioPreview(a.selected.path, clips)
	if err != nil {
		a.status = "无法开始试听 · " + err.Error()
		return false
	}
	a.player.Close()
	a.player = p
	return true
}

func (a *app) setPreviewMode(original bool) {
	playing := a.player != nil && a.player.Playing()
	if playing {
		a.player.Pause()
		a.updatePlaybackPosition()
	}
	a.originalPreview = original
	a.markProject()
	if playing {
		if _, ok := nextRetained(a.selected.history.Clips, a.position); original || ok {
			a.togglePlay()
		}
	}
}

func (a *app) previewPlayable(kept int64) bool {
	return a.selected != nil && !a.selected.offline && !a.busy() && a.player != nil &&
		(a.video == nil || a.video.Ready()) && (a.originalPreview || kept > 0)
}

func nextRetained(clips []timeline.Clip, at int64) (int64, bool) {
	i := clipAt(clips, at)
	if i < len(clips) && !clips[i].Keep {
		i++
	}
	if i < len(clips) && clips[i].Keep {
		return max(at, clips[i].Start), true
	}
	return 0, false
}

const previewTickInterval = 16 * time.Millisecond

func upcomingVideoCut(clips []timeline.Clip, at int64) (timeline.Span, bool) {
	i := clipAt(clips, at)
	if i+1 < len(clips) && clips[i].Keep && !clips[i+1].Keep {
		return clips[i+1].Span, true
	}
	return timeline.Span{}, false
}

func (a *app) updatePlaybackPosition() bool {
	if a.player == nil {
		return false
	}
	playing := a.player.Playing()
	if !playing && !a.wasPlaying {
		return false
	}
	a.wasPlaying = playing
	position := max(0, min(a.player.Position().Microseconds(), a.selected.info.Duration))
	if !a.videoSeekAt.IsZero() {
		// Native seeks can report the previous timestamp until the decoder lands.
		elapsed := time.Since(a.videoSeekAt)
		delta := position - a.position
		if delta < -1_000 || delta > elapsed.Microseconds()+100_000 {
			if elapsed < 2*time.Second {
				return playing
			}
			a.stopPlayback()
			a.videoSeekAt = time.Time{}
			a.status = "视频定位超时，请重新定位后播放"
			return false
		}
		a.videoSeekAt = time.Time{}
	}
	if a.selected.info.Video {
		position = max(a.position, position)
	}
	if a.selected.info.Video && !a.originalPreview {
		clips := a.selected.history.Clips
		if len(clips) > 0 {
			cutPosition := position
			// A complete short cut could otherwise fit between two UI
			// updates. Request that seek before entering the removed range.
			if playing {
				if cut, ok := upcomingVideoCut(clips, position); ok &&
					cut.End-cut.Start <= previewTickInterval.Microseconds() &&
					cut.Start-position <= previewTickInterval.Microseconds() {
					cutPosition = cut.Start
				}
			}
			last := clips[len(clips)-1]
			if !last.Keep && cutPosition >= last.Start {
				a.stopPlayback()
				a.seek(last.Start)
				position, playing = last.Start, false
			} else if playing {
				if target, ok := nextRetained(clips, cutPosition); ok && target > cutPosition {
					a.seek(target)
					position = target
				}
			}
		}
	}
	a.position = position
	if !a.overviewDrag.active {
		a.followPlayhead()
	}
	a.scheduleRecovery()
	return playing
}
