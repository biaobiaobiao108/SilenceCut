package media

/*
#include "ffmpeg.h"
*/
import "C"

import (
	"context"
	"errors"
	"math"
	"unsafe"

	"silencecut/internal/timeline"
)

type pauseWindow struct {
	level, voicedLevel, peak float32
	pitch                    int
}

func detectPauses(ctx context.Context, in *C.CutInput, info Info, settings timeline.Settings, progress Progress) ([]timeline.Clip, error) {
	rate, channels := info.Rate, info.Channels
	windowSize := (rate + 39) / 40 // 25 ms, rounded up to a whole sample
	limit := sampleIndex(info.Duration, rate)
	buffer := make([]float32, windowSize*channels)
	stride := max(1, rate/4000)
	pitch := make([]float64, windowSize/stride)
	var windows []pauseWindow
	var cursor, decodedEnd int64
	var received bool
	consume := func(count int64, pcm []float32) {
		for count > 0 && ctx.Err() == nil {
			offset := int(cursor % int64(windowSize))
			width := min(int64(windowSize-offset), count)
			dst := buffer[offset*channels : (offset+int(width))*channels]
			if pcm == nil {
				clear(dst)
			} else {
				copy(dst, pcm[:len(dst)])
				pcm = pcm[len(dst):]
			}
			cursor += width
			count -= width
			if cursor%int64(windowSize) == 0 || cursor == limit {
				if pcm == nil && offset == 0 {
					windows = append(windows, pauseWindow{level: -120, voicedLevel: -120, peak: -120})
				} else {
					windows = append(windows, describePauseWindow(buffer[:(offset+int(width))*channels], pitch, rate, stride, channels))
				}
			}
		}
	}
	err := frames(ctx, in, func(_ C.int, frame *C.CutFrame) (bool, error) {
		if frame.samples == 0 {
			return false, nil
		}
		received = true
		start := audioFrameStart(sampleIndex(int64(frame.pts), rate), decodedEnd, rate)
		end := start + int64(frame.samples)
		decodedEnd = max(decodedEnd, end)
		if start > cursor {
			consume(min(limit, start)-cursor, nil)
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if end = min(end, limit); end > cursor {
			pcm := unsafe.Slice((*float32)(unsafe.Pointer(frame.audio)), int(frame.samples)*channels)
			consume(end-cursor, pcm[int(cursor-start)*channels:int(end-start)*channels])
		}
		progress(fraction(cursor, limit))
		return cursor == limit, nil
	})
	if err != nil {
		return nil, err
	}
	if !received {
		return nil, errors.New("音轨没有可解码的数据")
	}
	consume(limit-cursor, nil)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	reference, hasVoice := speechLevel(windows, settings.Threshold)
	voiceFloor := max(-95, min(reference-28, float32(settings.Threshold)-8))
	clearVoice := reference - 18
	breathLimit := reference - float32(settings.BreathMargin)
	minimum := settings.MinPause
	if !hasVoice {
		// Keep the peak-based behavior for whisper-only and other material
		// with no reliable voiced reference.
		minimum = max(minimum, 100_000)
	}
	at := func(i int) int64 {
		return min(info.Duration, int64(math.Round(float64(i)*float64(windowSize)*float64(timeline.Second)/float64(rate))))
	}
	var gaps []timeline.Span
	start := 0
	for i, window := range windows {
		pause := window.peak < float32(settings.Threshold)
		if hasVoice {
			voice := window.voicedLevel >= clearVoice ||
				window.voicedLevel >= voiceFloor && sustainedPitch(windows, i)
			pause = !voice && window.level < breathLimit
		}
		if !pause {
			if at(i)-at(start) >= minimum {
				gaps = append(gaps, timeline.Span{Start: at(start), End: at(i)})
			}
			start = i + 1
		}
	}
	if info.Duration-at(start) >= minimum {
		gaps = append(gaps, timeline.Span{Start: at(start), End: info.Duration})
	}
	return timeline.Filter(info.Duration, gaps, settings), nil
}

func describePauseWindow(pcm []float32, pitch []float64, rate, stride, channels int) pauseWindow {
	result := pauseWindow{level: -120, voicedLevel: -120, peak: -120}
	samples := len(pcm) / channels
	pitch = pitch[:samples/stride]
	for ch := range channels {
		var energy, peak float64
		for i := ch; i < len(pcm); i += channels {
			x := float64(pcm[i])
			energy += x * x
			peak = max(peak, math.Abs(x))
		}
		if energy <= 0 || math.IsNaN(energy) {
			continue
		}
		level := float32(10 * math.Log10(energy/float64(samples)))
		result.level = max(result.level, level)
		result.peak = max(result.peak, float32(20*math.Log10(peak)))
		if level <= result.voicedLevel || level < -95 {
			continue
		}
		// Average adjacent samples before measuring pitch. This keeps
		// high-frequency turbulence from dominating the correlation.
		var mean float64
		for i := range pitch {
			var sum float64
			start := i*stride*channels + ch
			for j := range stride {
				sum += float64(pcm[start+j*channels])
			}
			pitch[i] = sum
			mean += sum
		}
		if len(pitch) == 0 {
			continue
		}
		mean /= float64(len(pitch))
		for i := range pitch {
			pitch[i] -= mean
		}
		if hz := pitchFrequency(pitch, rate/stride); hz > 0 {
			result.voicedLevel, result.pitch = level, hz
		}
	}
	return result
}

func pitchFrequency(samples []float64, rate int) int {
	// A voiced period has a correlation peak after a trough. A rising
	// or falling noise envelope can correlate at short lags without one.
	shortest := max(1, rate/400)
	var trough bool
	var previous float64
	for lag := max(1, shortest/2); lag <= min(rate/70, len(samples)*3/5); lag++ {
		var dot, power float64
		for i, left := range samples[:len(samples)-lag] {
			right := samples[i+lag]
			dot += left * right
			power += left*left + right*right
		}
		if power == 0 {
			continue
		}
		score := dot / power
		if trough && lag > shortest && previous >= .38 && score < previous {
			return rate / (lag - 1)
		}
		trough = trough || score < .10
		previous = score
	}
	return 0
}

func sustainedPitch(windows []pauseWindow, i int) bool {
	// Quiet voiced syllables carry similar pitch across adjacent windows.
	// Isolated correlation peaks also occur in breaths and room noise.
	hz := windows[i].pitch
	matches := 0
	for j := max(0, i-2); j < min(len(windows), i+3); j++ {
		other := windows[j].pitch
		if j != i && other*5 >= hz*4 && other*4 <= hz*5 {
			matches++
		}
	}
	return matches >= 2
}

func speechLevel(windows []pauseWindow, fallback float64) (float32, bool) {
	const lowest, highest = -96, 12
	var bins [highest - lowest + 1]int
	var count int
	for _, window := range windows {
		if window.voicedLevel >= float32(max(-95, fallback-12)) {
			bin := int(math.Round(float64(window.voicedLevel)))
			bins[min(highest, max(lowest, bin))-lowest]++
			count++
		}
	}
	if count < 3 {
		return 0, false
	}
	rank := (count*3 + 3) / 4
	for i, n := range bins {
		rank -= n
		if rank <= 0 {
			return float32(i + lowest), true
		}
	}
	return 0, false
}
