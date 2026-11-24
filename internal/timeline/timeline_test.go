package timeline

import (
	"reflect"
	"testing"
)

func TestAutomaticThreshold(t *testing.T) {
	for _, tt := range []struct {
		name  string
		peaks []Peak
		want  float64
	}{
		{"digital silence", []Peak{{}, {}}, -70},
		{"quiet signal", []Peak{{Min: -100}, {Max: 110}, {}}, -65},
		{"constant background", []Peak{{Max: 100}, {Min: -100}, {Max: 100}, {Max: 4096}, {Min: -4096}}, -44},
		{"single least significant bit", []Peak{{Max: 1}}, -96},
		{"full scale negative", []Peak{{Min: -32768}}, -36},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := AutomaticThreshold(tt.peaks); got != tt.want {
				t.Fatalf("threshold = %.1f dB, want %.1f dB", got, tt.want)
			}
		})
	}
}

func TestFilter(t *testing.T) {
	const duration = 4 * Second
	gaps := []Span{{0, Second}, {1500000, 2 * Second}, {3 * Second, duration}}
	tests := []struct {
		name string
		gaps []Span
		opts Settings
		want []Span
	}{
		{"all cuts", gaps, Settings{Before: 100000, After: 200000}, []Span{{900000, 1700000}, {1900000, 3200000}}},
		{"ends only", gaps, Settings{Before: 100000, After: 200000, OnlyEnds: true}, []Span{{900000, 3200000}}},
		{"padding consumes middle silence", gaps, Settings{Before: 250000, After: 250000}, []Span{{750000, 3250000}}},
		{"entirely quiet", []Span{{0, duration}}, Settings{Before: 100000, After: 200000}, nil},
		{"entirely audible", nil, Settings{}, []Span{{0, duration}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := Kept(Filter(duration, tt.gaps, tt.opts))
			if !reflect.DeepEqual(actual, tt.want) {
				t.Fatalf("got %+v, want %+v", actual, tt.want)
			}
		})
	}
}

func TestEditUndoRedo(t *testing.T) {
	initial := []Clip{{Span{0, 10}, true}, {Span{10, 20}, false}, {Span{20, 30}, true}}
	h := History{Clips: initial}
	h.Edit(Span{5, 25}, false)
	want := []Clip{{Span{0, 5}, true}, {Span{5, 25}, false}, {Span{25, 30}, true}}
	if !reflect.DeepEqual(h.Clips, want) {
		t.Fatalf("edited: %+v", h.Clips)
	}
	h.Undo()
	if !reflect.DeepEqual(h.Clips, initial) {
		t.Fatalf("undo: %+v", h.Clips)
	}
	h.Redo()
	if !reflect.DeepEqual(h.Clips, want) {
		t.Fatalf("redo: %+v", h.Clips)
	}
}
