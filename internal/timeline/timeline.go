package timeline

import (
	"math"
	"slices"
)

const Second int64 = 1_000_000

type Span struct {
	Start, End int64
}

type Clip struct {
	Span
	Keep bool
}

type Settings struct {
	Threshold     float64
	Before, After int64
	OnlyEnds      bool
}

func Filter(duration int64, silence []Span, settings Settings) []Clip {
	var clips []Clip
	at := int64(0)
	for _, gap := range silence {
		start, end := gap.Start, gap.End
		if start > 0 {
			start += settings.After
		}
		if end < duration {
			end -= settings.Before
		}
		if settings.OnlyEnds && gap.Start > 0 && gap.End < duration {
			continue
		}
		start = max(at, min(duration, start))
		end = min(duration, end)
		if end <= start {
			continue
		}
		if start > at {
			clips = append(clips, Clip{Span{at, start}, true})
		}
		clips = append(clips, Clip{Span{start, end}, false})
		at = end
	}
	if at < duration {
		clips = append(clips, Clip{Span{at, duration}, true})
	}
	return clips
}

func SetKeep(clips []Clip, span Span, keep bool) []Clip {
	var result []Clip
	for _, clip := range clips {
		start, end := max(clip.Start, span.Start), min(clip.End, span.End)
		if clip.Keep == keep || start >= end {
			result = appendClip(result, clip)
			continue
		}
		result = appendClip(result, Clip{Span{clip.Start, start}, clip.Keep})
		result = appendClip(result, Clip{Span{start, end}, keep})
		result = appendClip(result, Clip{Span{end, clip.End}, clip.Keep})
	}
	return result
}

func appendClip(clips []Clip, clip Clip) []Clip {
	if clip.Start >= clip.End {
		return clips
	}
	if n := len(clips); n > 0 && clips[n-1].Keep == clip.Keep {
		clips[n-1].End = clip.End
		return clips
	}
	return append(clips, clip)
}

func Kept(clips []Clip) []Span {
	var result []Span
	for _, clip := range clips {
		if clip.Keep {
			result = append(result, clip.Span)
		}
	}
	return result
}

type History struct {
	Clips []Clip
	undo  [][]Clip
	redo  [][]Clip
}

func (h *History) Edit(span Span, keep bool) {
	updated := SetKeep(h.Clips, span, keep)
	if slices.Equal(updated, h.Clips) {
		return
	}
	h.undo = append(h.undo, h.Clips)
	if len(h.undo) > 20 {
		h.undo = h.undo[1:]
	}
	h.redo, h.Clips = nil, updated
}

func (h *History) CanUndo() bool { return len(h.undo) > 0 }
func (h *History) CanRedo() bool { return len(h.redo) > 0 }

func (h *History) Undo() {
	if h.CanUndo() {
		h.redo = append(h.redo, h.Clips)
		h.Clips, h.undo = h.undo[len(h.undo)-1], h.undo[:len(h.undo)-1]
	}
}

func (h *History) Redo() {
	if h.CanRedo() {
		h.undo = append(h.undo, h.Clips)
		h.Clips, h.redo = h.redo[len(h.redo)-1], h.redo[:len(h.redo)-1]
	}
}

const PeakSamples = 256

type Peak struct{ Min, Max int16 }

func AutomaticThreshold(peaks []Peak, sampleRate int) float64 {
	var sum float64
	var count int
	for _, peak := range peaks {
		// The reference threshold formula takes 8-bit peaks. Keep 16 bits for drawing.
		magnitude := (math.Abs(float64(peak.Min/256)) + math.Abs(float64(peak.Max/256))) / 2
		if magnitude > 0 {
			sum += 5 * math.Log10(magnitude/(float64(sampleRate)*PeakSamples))
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return math.Round(sum/float64(count)*10) / 10
}
