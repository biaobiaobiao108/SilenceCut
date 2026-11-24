package main

import (
	"io"
	"slices"
	"testing"

	"silencecut/internal/timeline"
)

type sampleSource struct {
	samples []float32
	at      int
}

func (s *sampleSource) SampleRate() int { return 1000 }
func (s *sampleSource) Channels() int   { return 1 }
func (s *sampleSource) Len() int64      { return int64(len(s.samples)) }
func (s *sampleSource) SetPosition(at int64) error {
	s.at = int(at)
	return nil
}
func (s *sampleSource) Read(buf []float32) (int, error) {
	n := copy(buf, s.samples[s.at:])
	s.at += n
	if s.at == len(s.samples) {
		return n, io.EOF
	}
	return n, nil
}

func TestAuditionReadsOnlyRetainedSamples(t *testing.T) {
	// The loud samples stand in for a manually removed sound, including at both ends.
	src := &sampleSource{samples: []float32{1, .1, .2, 1, 1, .3, .4, 1}}
	clips := []timeline.Clip{
		{Span: timeline.Span{Start: 1000, End: 3000}, Keep: true},
		{Span: timeline.Span{Start: 5000, End: 7000}, Keep: true},
	}
	d := newAuditionDecoder(src, nil, clips)
	buf := make([]float32, 10)
	n, err := d.Read(buf)
	if err != nil || d.Len() != 4 || !slices.Equal(buf[:n], []float32{.1, .2, .3, .4}) {
		t.Fatalf("rendered samples = %v, length %d, error %v", buf[:n], d.Len(), err)
	}
	if n, err = d.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("read after retained end returned %d, %v", n, err)
	}
	if err := d.SetPosition(2); err != nil {
		t.Fatal(err)
	}
	n, err = d.Read(buf)
	if err != nil || !slices.Equal(buf[:n], []float32{.3, .4}) {
		t.Fatalf("seek across removed region = %v, error %v", buf[:n], err)
	}
}
