package media

/*
#cgo LDFLAGS: -lavfilter -lavformat -lavcodec -lswscale -lswresample -lavutil
#include <stdlib.h>
#include <libavutil/log.h>
#include "ffmpeg.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"silencecut/internal/timeline"
)

type Info struct {
	Duration        int64
	Rate, Channels  int
	Video           bool
	Width, Height   int
	hardwareDecoder bool
}

type Progress func(float64)

var ffmpegDebug = os.Getenv("SILENCECUT_FFMPEG_DEBUG") == "1"

func init() {
	level := C.int(C.AV_LOG_ERROR)
	if ffmpegDebug {
		level = C.AV_LOG_VERBOSE
	}
	C.av_log_set_level(level)
}

func avError(action string, code C.int) error {
	var buf [256]C.char
	C.cut_error(code, &buf[0], C.int(len(buf)))
	return fmt.Errorf("%s（%s）", action, C.GoString(&buf[0]))
}

func open(path string, mode C.int) (*C.CutInput, Info, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, Info{}, err
	}
	if !stat.Mode().IsRegular() {
		return nil, Info{}, errors.New("请选择本地音视频文件")
	}
	name := C.CString(path)
	defer C.free(unsafe.Pointer(name))
	var metadata C.CutInfo
	var code C.int
	input := C.cut_open(name, mode, &metadata, &code)
	if input == nil {
		return nil, Info{}, avError("无法读取素材", code)
	}
	info := Info{
		Duration: int64(metadata.duration), Rate: int(metadata.rate), Channels: int(metadata.channels),
		Video: metadata.video != 0, Width: int(metadata.width), Height: int(metadata.height),
		hardwareDecoder: metadata.hardware_decoder != 0,
	}
	if info.Channels > 64 {
		C.cut_close(input)
		return nil, Info{}, errors.New("音轨的声道数超过 64")
	}
	return input, info, nil
}

func Waveform(ctx context.Context, path string, progress Progress) (Info, []timeline.Peak, error) {
	in, info, err := open(path, C.CUT_AUDIO_ONLY)
	if err != nil {
		return Info{}, nil, err
	}
	defer C.cut_close(in)
	var peaks []timeline.Peak
	var until int64
	err = frames(ctx, in, func(kind C.int, frame *C.CutFrame) (bool, error) {
		if frame.samples == 0 {
			return false, nil
		}
		start := sampleIndex(int64(frame.pts), info.Rate)
		channels := info.Channels
		pcm := unsafe.Slice((*float32)(unsafe.Pointer(frame.audio)), int(frame.samples)*channels)
		end := start + int64(frame.samples)
		for pos := max(until, start); pos < end; pos++ {
			bin := int(pos / timeline.PeakSamples)
			for len(peaks) <= bin {
				peaks = append(peaks, timeline.Peak{})
			}
			var level float32
			offset := int(pos-start) * channels
			for _, sample := range pcm[offset : offset+channels] {
				level += sample
			}
			value := int16(max(-32768, min(32767, float64(level)/float64(channels)*32768)))
			peak := &peaks[bin]
			peak.Min = min(peak.Min, value)
			peak.Max = max(peak.Max, value)
		}
		until = max(until, end)
		progress(fraction(int64(frame.pts), info.Duration))
		return false, nil
	})
	if err == nil && len(peaks) == 0 {
		err = errors.New("音轨没有可解码的数据")
	}
	return info, peaks, err
}

func Detect(ctx context.Context, path string, settings timeline.Settings, progress Progress) ([]timeline.Clip, error) {
	in, info, err := open(path, C.CUT_AUDIO_ONLY)
	if err != nil {
		return nil, err
	}
	defer C.cut_close(in)
	var gaps []timeline.Span
	var cursor, quiet int64
	var hasAudio bool
	minQuiet := int64((info.Rate + 9) / 10)
	threshold := float32(math.Pow(10, settings.Threshold/20))
	toTime := func(sample int64) int64 {
		return (sample*timeline.Second + int64(info.Rate)/2) / int64(info.Rate)
	}
	finishQuiet := func(end int64) {
		if quiet >= minQuiet {
			gaps = appendGap(gaps, toTime(cursor-quiet), end, info.Duration)
		}
		quiet = 0
	}
	// Some containers round packet timestamps to milliseconds.
	jitter := int64((info.Rate + 999) / 1000)
	err = frames(ctx, in, func(kind C.int, frame *C.CutFrame) (bool, error) {
		if frame.samples == 0 {
			return false, nil
		}
		start := sampleIndex(int64(frame.pts), info.Rate)
		if abs := max(start-cursor, cursor-start); abs <= jitter {
			start = cursor
		}
		if start > cursor {
			quiet += start - cursor
			cursor = start
		}
		channels := info.Channels
		pcm := unsafe.Slice((*float32)(unsafe.Pointer(frame.audio)), int(frame.samples)*channels)
		for i := max(0, cursor-start); i < int64(frame.samples); i++ {
			at := int(i) * channels
			silent := true
			for _, sample := range pcm[at : at+channels] {
				if !(sample > -threshold && sample < threshold) {
					silent = false
					break
				}
			}
			if silent {
				quiet++
			} else if quiet > 0 {
				finishQuiet(toTime(cursor))
			}
			cursor++
		}
		hasAudio = true
		progress(fraction(int64(frame.pts), info.Duration))
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if !hasAudio {
		return nil, errors.New("音轨没有可解码的数据")
	}
	end := max(cursor, sampleIndex(info.Duration, info.Rate))
	quiet += end - cursor
	cursor = end
	finishQuiet(info.Duration)
	return timeline.Filter(info.Duration, gaps, settings), nil
}

func appendGap(gaps []timeline.Span, start, end, duration int64) []timeline.Span {
	start, end = max(0, start), min(duration, end)
	if end <= start {
		return gaps
	}
	if n := len(gaps); n > 0 && gaps[n-1].End >= start {
		gaps[n-1].End = max(gaps[n-1].End, end)
		return gaps
	}
	return append(gaps, timeline.Span{Start: start, End: end})
}

func frames(ctx context.Context, in *C.CutInput, process func(C.int, *C.CutFrame) (bool, error)) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var frame C.CutFrame
		kind := C.cut_next(in, &frame)
		switch {
		case kind < 0:
			if frame.seek_error != 0 {
				return errGapSeek
			}
			return pipelineError("解码失败", kind, frame.video_error)
		case kind == 0:
			return nil
		}
		if stop, err := process(kind, &frame); stop || err != nil {
			return err
		}
	}
}

func Extension(info Info) string {
	if info.Video {
		return ".mp4"
	}
	return ".wav"
}

// Export writes to a temporary file in the destination directory and publishes only a closed muxer.
func Export(ctx context.Context, source, dir, base string, spans []timeline.Span, proxy bool, progress Progress) (string, error) {
	software := os.Getenv("SILENCECUT_VIDEO_ACCELERATION") == "software"
	return export(ctx, source, dir, base, spans, proxy, software, true, progress)
}

type videoPipelineError struct {
	error
	decode bool
}

func pipelineError(action string, code, video C.int) error {
	err := avError(action, code)
	if video != 0 {
		return videoPipelineError{err, video == C.CUT_VIDEO_DECODE_ERROR}
	}
	return err
}

var errGapSeek = errors.New("定位后无法恢复完整的音视频区间")

func export(ctx context.Context, source, dir, base string, spans []timeline.Span, proxy, software, skipGaps bool, progress Progress) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(spans) == 0 {
		return "", errors.New("没有要导出的区间")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	mode := C.int(C.CUT_DIRECT)
	if software {
		mode = C.CUT_SOFTWARE
	}
	in, info, err := open(source, mode)
	if err != nil {
		return "", err
	}
	defer func() { C.cut_close(in) }()
	var previous int64
	audioRanges := make([]outputSpan, 0, len(spans))
	videoRanges := make([]outputSpan, 0, len(spans))
	var duration int64
	for _, span := range spans {
		if span.Start < previous || span.End <= span.Start || span.End > info.Duration {
			return "", errors.New("区间超出素材范围，请重新导入")
		}
		videoRanges = append(videoRanges, outputSpan{span.Start, span.End, duration, span.End - span.Start})
		start, end := sampleIndex(span.Start, info.Rate), sampleIndex(span.End, info.Rate)
		outputStart := sampleIndex(duration, info.Rate)
		duration += span.End - span.Start
		length := sampleIndex(duration, info.Rate) - outputStart
		audioRanges = append(audioRanges, outputSpan{start, end, outputStart, length})
		previous = span.End
	}

	ext := Extension(info)
	temp, err := os.CreateTemp(dir, ".silencecut-*"+ext)
	if err != nil {
		return "", err
	}
	path := temp.Name()
	defer os.Remove(path)
	if err := temp.Close(); err != nil {
		return "", err
	}
	reported := -1.0
	report := func(value float64) {
		if value > reported {
			reported = value
			progress(value)
		}
	}
	for {
		if mode >= C.CUT_HARDWARE && !info.hardwareDecoder {
			// The current attempt already uses software decoding and hardware encoding.
			mode = C.CUT_HARDWARE_ENCODER
		}
		err = encode(ctx, in, info, path, proxy, skipGaps, audioRanges, videoRanges, report)
		C.cut_close(in)
		in = nil
		if err == nil {
			break
		}
		if canceled := ctx.Err(); canceled != nil {
			return "", canceled
		}
		if errors.Is(err, errGapSeek) {
			skipGaps = false
		} else {
			var videoError videoPipelineError
			if !info.Video || mode == C.CUT_SOFTWARE || !errors.As(err, &videoError) {
				return "", err
			}
			if videoError.decode && mode >= C.CUT_HARDWARE {
				mode = C.CUT_HARDWARE_ENCODER
			} else {
				mode--
			}
		}
		if ffmpegDebug {
			fmt.Fprintf(os.Stderr, "SilenceCut export retry: mode=%d indexed-seek=%t cause=%v\n", mode, skipGaps, err)
		}
		var retryInfo Info
		in, retryInfo, err = open(source, mode)
		if err != nil {
			return "", err
		}
		info.hardwareDecoder = retryInfo.hardwareDecoder
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	for index := 1; ; index++ {
		name := base
		if index > 1 {
			name = fmt.Sprintf("%s (%d)", base, index)
		}
		final := filepath.Join(dir, name+ext)
		err := publish(path, final)
		if !errors.Is(err, os.ErrExist) {
			return final, err
		}
	}
}

type outputSpan struct {
	start, end, output, length int64
}

func encode(ctx context.Context, in *C.CutInput, info Info, path string, proxy, skipGaps bool, audioRanges, videoRanges []outputSpan, progress Progress) error {
	name := C.CString(path)
	defer C.free(unsafe.Pointer(name))
	var code, proxyFlag, videoError C.int
	if proxy {
		proxyFlag = 1
	}
	out := C.cut_create(in, name, proxyFlag, &code, &videoError)
	if out == nil {
		return pipelineError("无法创建导出文件", code, videoError)
	}
	defer C.cut_discard(out)
	first, last := videoRanges[0].start, videoRanges[len(videoRanges)-1].end
	audio, video := audioWriter{out: out, ranges: audioRanges, channels: info.Channels}, 0
	var pictureTime int64
	var hasPicture bool
	var jumps, decodedVideo int
	var checkAudio, checkVideo bool
	lastChecked := -1
	trySkip := func(after int64) error {
		if !skipGaps || !info.Video || audio.index != video || video >= len(videoRanges) ||
			video == lastChecked || audio.written != 0 || checkAudio || checkVideo {
			return nil
		}
		lastChecked = video
		next := videoRanges[video].start
		var initial C.int
		if video == 0 {
			initial = 1
		}
		code := C.cut_skip_gap(in, C.int64_t(after), C.int64_t(next), initial)
		if code < 0 {
			return fmt.Errorf("%w（%v）", errGapSeek, avError("素材定位失败", code))
		}
		if code > 0 {
			jumps++
			checkAudio, checkVideo, hasPicture = true, true, false
		}
		return nil
	}
	if err := trySkip(0); err != nil {
		return err
	}
	draw := func(end int64) error {
		for video < len(videoRanges) && videoRanges[video].start < end {
			span := videoRanges[video]
			start, stop := max(pictureTime, span.start), min(end, span.end)
			if stop > start {
				if code := C.cut_video(out, in, C.int64_t(span.output+start-span.start), C.int64_t(stop-start), &videoError); code < 0 {
					return pipelineError("视频编码失败", code, videoError)
				}
			}
			if end < span.end {
				break
			}
			video++
		}
		return nil
	}
	started := time.Now()
	reportAt := time.Time{}
	var scannedThrough int64
	err := frames(ctx, in, func(kind C.int, frame *C.CutFrame) (bool, error) {
		pts := int64(frame.pts)
		scannedThrough = max(scannedThrough, pts)
		if kind == 1 {
			if checkAudio && frame.samples > 0 {
				if sampleIndex(pts, info.Rate) > audioRanges[lastChecked].start {
					return false, errGapSeek
				}
				checkAudio = false
			}
			if err := audio.write(sampleIndex(pts, info.Rate), frame); err != nil {
				return false, err
			}
		} else {
			decodedVideo++
			if checkVideo {
				if pts > videoRanges[lastChecked].start {
					return false, errGapSeek
				}
				checkVideo = false
			}
			if hasPicture {
				if pts <= pictureTime {
					return false, nil
				}
				if err := draw(pts); err != nil {
					return false, err
				}
			}
			if code := C.cut_hold_video(in); code < 0 {
				return false, avError("读取视频画面失败", code)
			}
			if hasPicture {
				pictureTime = pts
			} else {
				pictureTime, hasPicture = min(pts, videoRanges[video].start), true
			}
		}
		if err := trySkip(scannedThrough); err != nil {
			return false, err
		}
		if time.Since(reportAt) > 75*time.Millisecond {
			progress(min(.99, fraction(pts-first, last-first)))
			reportAt = time.Now()
		}
		return audio.index == len(audioRanges) && (!info.Video || video == len(videoRanges)), nil
	})
	if err != nil {
		var videoError videoPipelineError
		if errors.As(err, &videoError) && (checkAudio || checkVideo) {
			return fmt.Errorf("%w（%v）", errGapSeek, err)
		}
		return err
	}
	if checkAudio || checkVideo {
		return errGapSeek
	}
	if info.Video {
		if !hasPicture {
			return errors.New("视频没有可用的画面")
		}
		if err := draw(last); err != nil {
			return err
		}
	}
	for audio.index < len(audioRanges) {
		if err := audio.advance(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	videoError = 0
	if code := C.cut_finish(out, &videoError); code < 0 {
		return pipelineError("无法写入导出文件", code, videoError)
	}
	if ffmpegDebug && info.Video {
		fmt.Fprintf(os.Stderr, "SilenceCut export complete: seeks=%d decoded-video-frames=%d elapsed=%s\n",
			jumps, decodedVideo, time.Since(started).Round(time.Millisecond))
	}
	progress(1)
	return nil
}

type audioWriter struct {
	out      *C.CutOutput
	ranges   []outputSpan
	channels int
	index    int
	written  int64
}

func (a *audioWriter) write(start int64, frame *C.CutFrame) error {
	end := start + int64(frame.samples)
	for a.index < len(a.ranges) {
		span := a.ranges[a.index]
		if end <= span.start {
			break
		}
		from := max(start, span.start+a.written)
		to := min(end, span.end, span.start+span.length)
		if from < to {
			if err := a.silence(from - span.start - a.written); err != nil {
				return err
			}
			data := (*C.float)(unsafe.Add(unsafe.Pointer(frame.audio), (from-start)*int64(a.channels)*4))
			if code := C.cut_audio(a.out, data, C.int(to-from)); code < 0 {
				return avError("音频编码失败", code)
			}
			a.written += to - from
		}
		if end < span.end {
			break
		}
		if err := a.advance(); err != nil {
			return err
		}
	}
	return nil
}

func (a *audioWriter) silence(samples int64) error {
	for samples > 0 {
		n := min(samples, 32768)
		if code := C.cut_audio(a.out, nil, C.int(n)); code < 0 {
			return avError("无法补齐音频", code)
		}
		samples -= n
		a.written += n
	}
	return nil
}

func (a *audioWriter) advance() error {
	if err := a.silence(a.ranges[a.index].length - a.written); err != nil {
		return err
	}
	a.index++
	a.written = 0
	return nil
}

func sampleIndex(us int64, rate int) int64 {
	return int64(math.Round(float64(us) * float64(rate) / float64(timeline.Second)))
}

func fraction(done, total int64) float64 {
	return max(0, min(1, float64(done)/float64(total)))
}
