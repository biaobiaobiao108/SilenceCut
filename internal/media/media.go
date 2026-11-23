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
	Duration       int64
	Rate, Channels int
	Video          bool
	Width, Height  int
}

type Progress func(float64)

func init() {
	level := C.int(C.AV_LOG_ERROR)
	if os.Getenv("SILENCECUT_FFMPEG_DEBUG") == "1" {
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
	if code := C.cut_detect(in, C.double(settings.Threshold)); code < 0 {
		return nil, avError("无法启动静音检测", code)
	}
	var gaps []timeline.Span
	start := int64(math.MinInt64)
	lastAudioEnd := int64(0)
	err = frames(ctx, in, func(kind C.int, frame *C.CutFrame) (bool, error) {
		if frame.samples == 0 {
			return false, nil
		}
		pts := int64(frame.pts)
		if lastAudioEnd == 0 && pts >= timeline.Second/10 {
			gaps = appendGap(gaps, 0, pts, info.Duration)
		}
		lastAudioEnd = max(lastAudioEnd, pts+int64(frame.samples)*timeline.Second/int64(info.Rate))
		detectedStart, detectedEnd := int64(frame.silence_start), int64(frame.silence_end)
		if detectedEnd != math.MinInt64 && detectedStart >= detectedEnd && start != math.MinInt64 {
			gaps = appendGap(gaps, start, detectedEnd, info.Duration)
			start = math.MinInt64
		}
		if detectedStart != math.MinInt64 {
			start = detectedStart
		}
		if detectedEnd != math.MinInt64 && start != math.MinInt64 && start < detectedEnd {
			gaps = appendGap(gaps, start, detectedEnd, info.Duration)
			start = math.MinInt64
		}
		progress(fraction(pts, info.Duration))
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	if lastAudioEnd == 0 {
		return nil, errors.New("音轨没有可解码的数据")
	}
	if start != math.MinInt64 {
		gaps = appendGap(gaps, start, info.Duration, info.Duration)
	} else if info.Duration-lastAudioEnd >= timeline.Second/10 {
		gaps = appendGap(gaps, lastAudioEnd, info.Duration, info.Duration)
	}
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
			return avError("解码失败", kind)
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(spans) == 0 {
		return "", errors.New("没有要导出的区间")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	mode := C.int(C.CUT_DIRECT)
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
	for {
		err = encode(ctx, in, info, path, proxy, audioRanges, videoRanges, progress)
		C.cut_close(in)
		in = nil
		if err == nil {
			break
		}
		if canceled := ctx.Err(); canceled != nil {
			return "", canceled
		}
		if !info.Video || mode == C.CUT_SOFTWARE {
			return "", err
		}
		// A driver can reject a frame after opening the codecs. Re-decode from
		// the source and replace the temporary output before publishing.
		mode--
		in, _, err = open(source, mode)
		if err != nil {
			return "", err
		}
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

func encode(ctx context.Context, in *C.CutInput, info Info, path string, proxy bool, audioRanges, videoRanges []outputSpan, progress Progress) error {
	name := C.CString(path)
	defer C.free(unsafe.Pointer(name))
	var code, proxyFlag C.int
	if proxy {
		proxyFlag = 1
	}
	out := C.cut_create(in, name, proxyFlag, &code)
	if out == nil {
		return avError("无法创建导出文件", code)
	}
	defer C.cut_discard(out)
	first, last := videoRanges[0].start, videoRanges[len(videoRanges)-1].end
	if first > 5*timeline.Second {
		C.cut_seek(in, C.int64_t(first-2*timeline.Second))
	}
	audio, video := audioWriter{out: out, ranges: audioRanges, channels: info.Channels}, 0
	var pictureTime int64
	var hasPicture bool
	draw := func(end int64) error {
		for video < len(videoRanges) && videoRanges[video].start < end {
			span := videoRanges[video]
			start, stop := max(pictureTime, span.start), min(end, span.end)
			if stop > start {
				if code := C.cut_video(out, in, C.int64_t(span.output+start-span.start), C.int64_t(stop-start)); code < 0 {
					return avError("视频编码失败", code)
				}
			}
			if end < span.end {
				break
			}
			video++
		}
		return nil
	}
	reportAt := time.Time{}
	err := frames(ctx, in, func(kind C.int, frame *C.CutFrame) (bool, error) {
		pts := int64(frame.pts)
		if kind == 1 {
			if err := audio.write(sampleIndex(pts, info.Rate), frame); err != nil {
				return false, err
			}
		} else {
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
				pictureTime, hasPicture = min(pts, first), true
			}
		}
		if time.Since(reportAt) > 75*time.Millisecond {
			progress(fraction(pts-first, last-first))
			reportAt = time.Now()
		}
		return audio.index == len(audioRanges) && (!info.Video || video == len(videoRanges)), nil
	})
	if err != nil {
		return err
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
	if code := C.cut_finish(out); code < 0 {
		return avError("无法写入导出文件", code)
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
