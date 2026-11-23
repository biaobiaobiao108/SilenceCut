package timeline

import (
	"reflect"
	"testing"
)

func TestAutomaticThresholdUsesEightBitPeaks(t *testing.T) {
	peaks := []Peak{{Min: -4096, Max: 4096}, {Min: -100, Max: 100}, {}}
	if got := AutomaticThreshold(peaks, 48000); got != -29.4 {
		t.Fatalf("threshold = %.1f dB, want -29.4 dB", got)
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
