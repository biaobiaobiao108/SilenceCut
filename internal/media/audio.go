package media

/*
#include "ffmpeg.h"
*/
import "C"

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"unsafe"

	"silencecut/internal/timeline"
)

// AudioDecoder presents source-clock PCM to the preview mixer. Its caller
// serializes Read, SetPosition and Close.
type AudioDecoder struct {
	path             string
	in               *C.CutInput
	info             Info
	length, position int64
	start, end       int64
	frame            C.CutFrame
	eof, seeking     bool
	issue            atomic.Pointer[error]
}

func OpenAudio(path string) (*AudioDecoder, error) {
	in, info, err := openAudioInput(path)
	if err != nil {
		return nil, err
	}
	return &AudioDecoder{path: path, in: in, info: info, length: sampleIndex(info.Duration, info.Rate)}, nil
}

func openAudioInput(path string) (*C.CutInput, Info, error) {
	in, info, err := open(context.Background(), path, C.CUT_AUDIO_ONLY, nil)
	if err == nil && info.Channels > 2 {
		// Mygo's mixer takes up to two channels. Mix surround channels
		// before handing it PCM so center-channel dialogue remains audible.
		C.cut_audio_stereo(in)
		info.Channels = 2
	}
	return in, info, err
}

func (d *AudioDecoder) SampleRate() int { return d.info.Rate }
func (d *AudioDecoder) Channels() int   { return d.info.Channels }
func (d *AudioDecoder) Len() int64      { return d.length }
func (d *AudioDecoder) Err() error {
	if err := d.issue.Load(); err != nil {
		return *err
	}
	return nil
}

func (d *AudioDecoder) SetPosition(frame int64) error {
	d.position = min(d.length, max(0, frame))
	return nil
}

func (d *AudioDecoder) Close() error {
	C.cut_close(d.in)
	d.in = nil
	return nil
}

func (d *AudioDecoder) restart() error {
	in, info, err := openAudioInput(d.path)
	if err != nil {
		return err
	}
	if info.Rate != d.info.Rate || info.Channels != d.info.Channels || info.Duration != d.info.Duration {
		C.cut_close(in)
		return errors.New("素材已改变，请先重新过滤")
	}
	C.cut_close(d.in)
	d.in = in
	d.frame = C.CutFrame{}
	d.start, d.end, d.eof, d.seeking = 0, 0, false, false
	return nil
}

func (d *AudioDecoder) Read(buf []float32) (int, error) {
	if d.in == nil {
		return 0, io.ErrClosedPipe
	}
	if d.position >= d.length {
		return 0, io.EOF
	}
	rate := int64(d.info.Rate)
	// Read through nearby cuts. Long jumps use decoder preroll so the target
	// can be reached without decoding the full gap.
	if d.position < d.start || d.eof && d.position < d.end || !d.eof && d.position-d.end > 3*rate {
		target := d.position - 2*rate
		if target > 0 && C.cut_seek_audio(d.in, C.int64_t(target*timeline.Second/rate)) >= 0 {
			d.start, d.end, d.eof, d.seeking = 0, 0, false, true
		} else if err := d.restart(); err != nil {
			d.issue.Store(&err)
			return 0, err
		}
	}

	channels := d.Channels()
	remaining := min(int64(len(buf)/channels), d.length-d.position)
	written := 0
	for remaining > 0 {
		if !d.eof && d.position >= d.end {
			if err := d.nextFrame(); err != nil {
				d.issue.Store(&err)
				return written, err
			}
			continue
		}
		count := remaining
		switch {
		case d.eof:
			clear(buf[written : written+int(count)*channels])
		case d.position < d.start:
			count = min(count, d.start-d.position)
			clear(buf[written : written+int(count)*channels])
		default:
			count = min(count, d.end-d.position)
			size := int(d.frame.samples) * channels
			pcm := unsafe.Slice((*float32)(unsafe.Pointer(d.frame.audio)), size)
			offset := int(d.position-d.start) * channels
			copy(buf[written:], pcm[offset:offset+int(count)*channels])
		}
		d.position += count
		written += int(count) * channels
		remaining -= count
	}
	return written, nil
}

func (d *AudioDecoder) nextFrame() error {
	for {
		d.frame = C.CutFrame{}
		kind := C.cut_next(d.in, &d.frame)
		if kind > 0 && d.frame.samples == 0 {
			continue
		}
		start := sampleIndex(int64(d.frame.pts), d.info.Rate)
		if d.seeking {
			d.seeking = false
			if kind <= 0 || start > d.position {
				// Some demuxers can seek past the requested sample.
				return d.restart()
			}
		} else {
			start = audioFrameStart(start, d.end, d.info.Rate)
		}
		if kind < 0 {
			return avError("试听解码失败", kind)
		}
		if kind == 0 {
			d.eof = true
			return nil
		}
		end := start + int64(d.frame.samples)
		if end > d.end {
			d.start, d.end = start, end
			return nil
		}
	}
}
