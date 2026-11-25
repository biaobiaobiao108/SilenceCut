package media

/*
#include "ffmpeg.h"
*/
import "C"

import (
	"context"
	"errors"
	"math"
	"slices"
	"time"
	"unsafe"

	"silencecut/internal/timeline"
)

type AudioAnalysis struct {
	Info      Info
	Peaks     []timeline.Peak
	Clips     []timeline.Clip
	Automatic float64
}

func Waveform(ctx context.Context, path string, progress Progress) (Info, []timeline.Peak, error) {
	result, err := analyzeAudio(ctx, path, true, nil, progress, nil)
	return result.Info, result.Peaks, err
}

func Detect(ctx context.Context, path string, settings timeline.Settings, progress Progress) ([]timeline.Clip, error) {
	result, err := analyzeAudio(ctx, path, false, &settings, progress, nil)
	return result.Clips, err
}

// AnalyzeAudio decodes once to build the waveform and detect the cuts.
// Pause presets derive their threshold from the completed waveform. On longer
// jobs the optional preview receives an independent copy of the opening waveform.
func AnalyzeAudio(ctx context.Context, path string, settings timeline.Settings, progress Progress, preview func(Info, []timeline.Peak)) (AudioAnalysis, error) {
	return analyzeAudio(ctx, path, true, &settings, progress, preview)
}

func analyzeAudio(ctx context.Context, path string, waveform bool, settings *timeline.Settings, progress Progress, preview func(Info, []timeline.Peak)) (AudioAnalysis, error) {
	started := time.Now()
	token, release, err := mediaCancellation(ctx)
	if err != nil {
		return AudioAnalysis{}, err
	}
	defer release()
	in, info, err := open(ctx, path, C.CUT_AUDIO_ONLY, token)
	if err != nil {
		return AudioAnalysis{}, err
	}
	defer C.cut_close(in)

	var peaks []timeline.Peak
	var pauses *pauseDetector
	var quiet *quietDetector
	if settings != nil {
		if settings.MinPause > 0 {
			pauses = newPauseDetector(info)
		} else {
			quiet = &quietDetector{info: info, threshold: float32(math.Pow(10, settings.Threshold/20))}
		}
	}
	var until int64
	var received bool
	const previewDuration = 30 * timeline.Second
	if info.Duration <= previewDuration {
		preview = nil
	}
	previewSamples := sampleIndex(previewDuration, info.Rate)
	previewBins := int((previewSamples + timeline.PeakSamples - 1) / timeline.PeakSamples)
	err = frames(ctx, in, func(_ C.int, frame *C.CutFrame) (bool, error) {
		if frame.samples == 0 {
			return false, nil
		}
		received = true
		pts := int64(frame.pts)
		start := audioFrameStart(sampleIndex(pts, info.Rate), until, info.Rate)
		end := start + int64(frame.samples)
		if end > until {
			pcm := unsafe.Slice((*float32)(unsafe.Pointer(frame.audio)), int(frame.samples)*info.Channels)
			pcm = pcm[int(max(0, until-start))*info.Channels:]
			start = max(start, until)
			if waveform {
				peaks = appendPeaks(peaks, start, pcm, info.Channels)
			}
			if pauses != nil && pauses.cursor < pauses.limit {
				pauses.add(ctx, start, pcm)
			}
			if quiet != nil {
				quiet.add(start, pcm)
			}
			until = end
		}
		if preview != nil && until >= previewSamples && time.Since(started) >= 250*time.Millisecond {
			preview(info, slices.Clone(peaks[:previewBins]))
			preview = nil
		}
		done := pauses != nil && pauses.cursor == pauses.limit
		if pauses != nil && !waveform {
			progress(fraction(pauses.cursor, pauses.limit))
		} else {
			progress(fraction(pts, info.Duration))
		}
		return done && !waveform, ctx.Err()
	})
	result := AudioAnalysis{Info: info, Peaks: peaks}
	if err != nil {
		return result, err
	}
	if !received || waveform && len(peaks) == 0 {
		return result, errors.New("音轨没有可解码的数据")
	}
	if waveform && settings != nil {
		result.Automatic = timeline.AutomaticThreshold(peaks)
	}
	if pauses != nil {
		resolved := *settings
		if waveform {
			resolved.Threshold = result.Automatic
		}
		result.Clips, err = pauses.clips(ctx, resolved)
	}
	if quiet != nil {
		result.Clips = quiet.clips(*settings)
	}
	if err := ctx.Err(); err != nil {
		result.Clips = nil
		return result, err
	}
	return result, err
}

func appendPeaks(peaks []timeline.Peak, start int64, pcm []float32, channels int) []timeline.Peak {
	const width = timeline.PeakSamples
	end := start + int64(len(pcm)/channels)
	if needed := int((end-1)/width) + 1 - len(peaks); needed > 0 {
		peaks = append(peaks, make([]timeline.Peak, needed)...)
	}
	for len(pcm) != 0 {
		count := min(len(pcm), int(width-start%width)*channels)
		peak := &peaks[start/width]
		for _, sample := range pcm[:count] {
			value := int16(max(-32768, min(32767, float64(sample)*32768)))
			peak.Min = min(peak.Min, value)
			peak.Max = max(peak.Max, value)
		}
		start += int64(count / channels)
		pcm = pcm[count:]
	}
	return peaks
}

type quietDetector struct {
	info          Info
	threshold     float32
	cursor, quiet int64
	gaps          []timeline.Span
}

func (d *quietDetector) add(start int64, pcm []float32) {
	d.quiet += start - d.cursor
	d.cursor = start
	channels := d.info.Channels
	for at := 0; at < len(pcm); at += channels {
		silent := true
		for _, sample := range pcm[at : at+channels] {
			if !(sample > -d.threshold && sample < d.threshold) {
				silent = false
				break
			}
		}
		if silent {
			d.quiet++
		} else if d.quiet > 0 {
			d.finishQuiet(d.time(d.cursor))
		}
		d.cursor++
	}
}

func (d *quietDetector) clips(settings timeline.Settings) []timeline.Clip {
	end := max(d.cursor, sampleIndex(d.info.Duration, d.info.Rate))
	d.quiet += end - d.cursor
	d.cursor = end
	d.finishQuiet(d.info.Duration)
	return timeline.Filter(d.info.Duration, d.gaps, settings)
}

func (d *quietDetector) finishQuiet(end int64) {
	if d.quiet >= int64((d.info.Rate+9)/10) {
		d.gaps = appendGap(d.gaps, d.time(d.cursor-d.quiet), end, d.info.Duration)
	}
	d.quiet = 0
}

func (d *quietDetector) time(sample int64) int64 {
	rate := int64(d.info.Rate)
	return (sample*timeline.Second + rate/2) / rate
}
