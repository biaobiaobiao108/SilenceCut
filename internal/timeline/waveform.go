package timeline

import (
	"math"
	"slices"
)

// Waveform keeps progressively coarser peak data for the overview and zoomed-out views.
type Waveform struct {
	levels    [][]Peak
	Amplitude float32
}

func BuildWaveform(peaks []Peak) Waveform {
	if len(peaks) == 0 {
		return Waveform{}
	}
	var histogram [1025]int
	var audible int
	for _, peak := range peaks {
		magnitude := max(-int(peak.Min), int(peak.Max))
		if magnitude > 0 {
			histogram[(magnitude+31)/32]++
			audible++
		}
	}
	// Scale to the 96th percentile so isolated spikes don't flatten quiet speech.
	target, amplitude := (audible*96+99)/100, 64
	for bin, count := range histogram {
		target -= count
		if target <= 0 {
			amplitude = max(amplitude, bin*32)
			break
		}
	}
	wave := Waveform{levels: [][]Peak{peaks}, Amplitude: float32(amplitude)}
	for len(peaks) > 1 {
		next := make([]Peak, (len(peaks)+1)/2)
		for i, peak := range peaks {
			p := &next[i/2]
			p.Min, p.Max = min(p.Min, peak.Min), max(p.Max, peak.Max)
		}
		wave.levels = append(wave.levels, next)
		peaks = next
	}
	return wave
}

// Columns returns peak envelopes sampled across a visible time span.
func (w Waveform) Columns(span Span, rate, columns int, dst []Peak) []Peak {
	dst = slices.Grow(dst[:0], columns)[:columns]
	clear(dst)
	if len(w.levels) == 0 || rate <= 0 || columns == 0 {
		return dst
	}
	binsPerUS := float64(rate) / float64(Second*PeakSamples)
	step := float64(span.End-span.Start) * binsPerUS / float64(columns)
	level := 0
	for step >= 4 && level+1 < len(w.levels) {
		step /= 2
		level++
	}
	start := float64(span.Start) * binsPerUS / float64(uint64(1)<<level)
	peaks := w.levels[level]
	for i := range dst {
		from := int(start + float64(i)*step)
		to := min(len(peaks), int(math.Ceil(start+float64(i+1)*step)))
		for j := from; j < to; j++ {
			dst[i].Min = min(dst[i].Min, peaks[j].Min)
			dst[i].Max = max(dst[i].Max, peaks[j].Max)
		}
	}
	return dst
}
