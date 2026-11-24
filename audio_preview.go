package main

import (
	"io"
	"math"
	"sort"
	"time"

	"github.com/egoist/mygo/audio"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

type auditionSpan struct {
	sourceStart, sourceEnd, editedStart int64
}

// auditionDecoder reads only retained PCM frames, even when the mixer reads ahead.
type auditionDecoder struct {
	audio.Decoder
	io.Closer
	spans  []auditionSpan
	index  int
	source int64
	length int64
}

func newAuditionDecoder(d audio.Decoder, closer io.Closer, clips []timeline.Clip) *auditionDecoder {
	out := &auditionDecoder{Decoder: d, Closer: closer}
	rate := int64(d.SampleRate())
	for _, clip := range clips {
		if !clip.Keep {
			continue
		}
		// Round cut points to source samples, as in the media exporter.
		start := (clip.Start*rate + timeline.Second/2) / timeline.Second
		end := (clip.End*rate + timeline.Second/2) / timeline.Second
		if d.Len() >= 0 {
			end = min(end, d.Len())
		}
		if end > start {
			out.spans = append(out.spans, auditionSpan{start, end, out.length})
			out.length += end - start
		}
	}
	return out
}

func (d *auditionDecoder) Len() int64 { return d.length }

func (d *auditionDecoder) SetPosition(frame int64) error {
	d.index = sort.Search(len(d.spans), func(i int) bool {
		s := d.spans[i]
		return s.editedStart+s.sourceEnd-s.sourceStart > frame
	})
	if d.index == len(d.spans) {
		return nil
	}
	s := d.spans[d.index]
	d.source = s.sourceStart + max(0, frame-s.editedStart)
	return d.Decoder.SetPosition(d.source)
}

func (d *auditionDecoder) Read(buf []float32) (int, error) {
	ch := d.Channels()
	var written int
	for len(buf)-written >= ch && d.index < len(d.spans) {
		s := d.spans[d.index]
		if d.source >= s.sourceEnd {
			d.index++
			if d.index == len(d.spans) {
				break
			}
			d.source = d.spans[d.index].sourceStart
			if err := d.Decoder.SetPosition(d.source); err != nil {
				return written, err
			}
			continue
		}
		if d.source < s.sourceStart {
			d.source = s.sourceStart
			if err := d.Decoder.SetPosition(d.source); err != nil {
				return written, err
			}
		}
		size := int(min(int64((len(buf)-written)/ch), s.sourceEnd-d.source)) * ch
		n, err := d.Decoder.Read(buf[written : written+size])
		d.source += int64(n / ch)
		written += n
		if err != nil && !(err == io.EOF && d.source == s.sourceEnd) {
			return written, err
		}
		if n == 0 && err == nil {
			return written, io.ErrNoProgress
		}
	}
	if written == 0 && d.index == len(d.spans) {
		return 0, io.EOF
	}
	return written, nil
}

type audioPreview struct {
	*audio.Player
	decoder *media.AudioDecoder
}

func (p *audioPreview) Err() error { return p.decoder.Err() }

type auditionPlayer struct {
	*audioPreview
	spans []auditionSpan
	rate  int64
	clips []timeline.Clip
}

func openAudioPreview(path string, clips []timeline.Clip) (player, error) {
	raw, err := media.OpenAudio(path)
	if err != nil {
		return nil, err
	}
	if clips == nil {
		return &audioPreview{audio.NewPlayer(raw), raw}, nil
	}
	d := newAuditionDecoder(raw, raw, clips)
	return &auditionPlayer{&audioPreview{audio.NewPlayer(d), raw}, d.spans, int64(raw.SampleRate()), clips}, nil
}

func (p *auditionPlayer) Position() time.Duration {
	frame := p.frameAt(p.Player.Position())
	i := sort.Search(len(p.spans), func(i int) bool { return p.spans[i].editedStart > frame }) - 1
	if i < 0 {
		return 0
	}
	s := p.spans[i]
	source := min(s.sourceEnd, s.sourceStart+frame-s.editedStart)
	return p.frameTime(source)
}

func (p *auditionPlayer) Seek(at time.Duration) {
	frame := p.frameAt(at)
	i := sort.Search(len(p.spans), func(i int) bool { return p.spans[i].sourceEnd > frame })
	if i == len(p.spans) {
		p.Player.Seek(p.Player.Duration())
		return
	}
	s := p.spans[i]
	edited := s.editedStart + max(0, frame-s.sourceStart)
	p.Player.Seek(p.frameTime(edited))
}

func (p *auditionPlayer) frameAt(at time.Duration) int64 {
	return int64(math.Round(at.Seconds() * float64(p.rate)))
}

func (p *auditionPlayer) frameTime(frame int64) time.Duration {
	return time.Duration(float64(frame) / float64(p.rate) * float64(time.Second))
}
