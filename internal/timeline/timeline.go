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

func AutomaticThreshold(peaks []Peak) float64 {
	// Quantize the magnitude distribution to dBFS. Exclude digital silence
	// when estimating the typical signal and the background level.
	const floor = -91
	var bins [1 - floor]int
	var count int
	for _, peak := range peaks {
		if magnitude := peak.Magnitude(); magnitude > 0 {
			level := int(math.Round(20 * math.Log10(magnitude)))
			bins[level-floor]++
			count++
		}
	}
	if count == 0 {
		return -70
	}
	percentile := func(percent int) float64 {
		rank := (count*percent + 99) / 100
		for i, n := range bins {
			rank -= n
			if rank <= 0 {
				return float64(floor + i)
			}
		}
		return 0
	}
	background, signal := percentile(20), percentile(90)
	threshold := min(-36, signal-16)
	// Follow a clearly separated background level while leaving room for
	// quiet speech above it.
	if background <= -36 && signal-background >= 16 {
		threshold = min(background+6, signal-12)
	}
	return max(-96, threshold)
}

func (p Peak) Magnitude() float64 {
	return float64(max(-int(p.Min), int(p.Max))) / 32768
}
