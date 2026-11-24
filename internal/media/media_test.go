package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"silencecut/internal/timeline"
)

func discardProgress(float64) {}

func toneFixture(t *testing.T) string {
	return stereoFixture(t, 12000, 0, func(int16) int16 { return 0 })
}

func stereoFixture(t *testing.T, peak, background int16, right func(int16) int16) string {
	t.Helper()
	const rate = 48000
	const length = rate * 2
	var wav bytes.Buffer
	wav.WriteString("RIFF")
	order := binary.LittleEndian
	_ = binary.Write(&wav, order, uint32(36+length*4))
	wav.WriteString("WAVEfmt ")
	for _, value := range []uint32{16, 2<<16 | 1, rate, rate * 4, 16<<16 | 4} {
		_ = binary.Write(&wav, order, value)
	}
	wav.WriteString("data")
	_ = binary.Write(&wav, order, uint32(length*4))
	for i := range length {
		amplitude := background
		if i >= rate/4 && i < rate*3/4 || i >= rate && i < rate*3/2 {
			amplitude = peak
		}
		v := int16(float64(amplitude) * math.Sin(float64(i)*2*math.Pi*440/rate))
		_ = binary.Write(&wav, order, v)
		_ = binary.Write(&wav, order, right(v))
	}
	path := filepath.Join(t.TempDir(), "两段口播.wav")
	if err := os.WriteFile(path, wav.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWAVRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := toneFixture(t)
	info, peaks, err := Waveform(ctx, path, discardProgress)
	if err != nil {
		t.Fatal(err)
	}
	if info.Duration != 2*timeline.Second || info.Rate != 48000 || info.Channels != 2 || len(peaks) != 375 {
		t.Fatalf("metadata %+v with %d peaks", info, len(peaks))
	}
	clips, err := Detect(ctx, path, timeline.Settings{Threshold: -40}, discardProgress)
	if err != nil {
		t.Fatal(err)
	}
	kept := timeline.Kept(clips)
	expected := []timeline.Span{{Start: 250000, End: 750000}, {Start: 1000000, End: 1500000}}
	if len(kept) != len(expected) {
		t.Fatalf("detected %+v", clips)
	}
	for i, s := range kept {
		if math.Abs(float64(s.Start-expected[i].Start)) > 25 || math.Abs(float64(s.End-expected[i].End)) > 25 {
			t.Fatalf("cut exceeds one 48 kHz sample: %+v", kept)
		}
	}
	want := []timeline.Span{{Start: 267500, End: 700000}, {Start: 1050000, End: 1440000}}
	dest := t.TempDir()
	one, err := Export(ctx, path, dest, "结果", want, false, discardProgress)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Export(ctx, path, dest, "结果", want, false, discardProgress)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(one) != "结果.wav" || filepath.Base(two) != "结果 (2).wav" {
		t.Fatalf("collision handling: %s %s", one, two)
	}
	rendered, _, err := Waveform(ctx, one, discardProgress)
	if err != nil {
		t.Fatal(err)
	}
	if rendered.Duration != 822500 || rendered.Rate != 48000 || rendered.Channels != 2 {
		t.Fatalf("unexpected merged result: %+v", rendered)
	}

	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	_, err = Export(canceled, path, dest, "已取消", want, false, func(float64) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(dest, "*"))
	if err != nil || !reflect.DeepEqual(files, []string{two, one}) {
		t.Fatalf("unexpected output directory: %v %v", files, err)
	}
}

func TestAutomaticDetectionUsesAllChannels(t *testing.T) {
	for _, tt := range []struct {
		name             string
		peak, background int16
		right            func(int16) int16
	}{
		{"opposite phase", 12000, 0, func(v int16) int16 { return -v }},
		{"one channel", 12000, 0, func(int16) int16 { return 0 }},
		{"quiet speech level", 40, 0, func(v int16) int16 { return v }},
		{"continuous background", 6000, 120, func(v int16) int16 { return v }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			path := stereoFixture(t, tt.peak, tt.background, tt.right)
			_, peaks, err := Waveform(ctx, path, discardProgress)
			if err != nil {
				t.Fatal(err)
			}
			threshold := timeline.AutomaticThreshold(peaks)
			clips, err := Detect(ctx, path, timeline.Settings{Threshold: threshold}, discardProgress)
			if err != nil {
				t.Fatal(err)
			}
			want := []timeline.Span{{Start: 250000, End: 750000}, {Start: 1000000, End: 1500000}}
			kept := timeline.Kept(clips)
			if len(kept) != len(want) {
				t.Fatalf("threshold %.1f dB retained %v, want %v", threshold, kept, want)
			}
			for i, span := range kept {
				if math.Abs(float64(span.Start-want[i].Start)) > 1500 || math.Abs(float64(span.End-want[i].End)) > 1500 {
					t.Fatalf("threshold %.1f dB retained %v, want %v", threshold, kept, want)
				}
			}
		})
	}
}

func TestDetectFrameBoundaries(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is used to generate Matroska fixtures")
	}
	for _, tc := range []struct {
		name, audio, filter string
		kept                []timeline.Span
	}{
		{
			name:   "two gaps in one decoded frame",
			audio:  "aevalsrc='0.2*(lt(n,80)+between(n,960,1039)+gte(n,1920))':s=8000:d=1",
			filter: "asetnsamples=n=4096:p=0",
			kept:   []timeline.Span{{End: 10000}, {Start: 120000, End: 130000}, {Start: 240000, End: 1000000}},
		},
		{
			name:   "missing audio between frames",
			audio:  "sine=frequency=440:sample_rate=8000:duration=2",
			filter: "aselect='not(between(t,0.5,1.5))'",
			kept:   []timeline.Span{{End: 512000}, {Start: 1536000, End: 2000000}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "gaps.mkv")
			out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", tc.audio,
				"-af", tc.filter, "-c:a", "pcm_s16le", source).CombinedOutput()
			if err != nil {
				t.Fatalf("fixture: %v\n%s", err, out)
			}
			clips, err := Detect(context.Background(), source, timeline.Settings{Threshold: -40}, discardProgress)
			if err != nil {
				t.Fatal(err)
			}
			if kept := timeline.Kept(clips); !reflect.DeepEqual(kept, tc.kept) {
				t.Fatalf("retained %v, want %v", kept, tc.kept)
			}
		})
	}
}

func TestHDRExportToneMapsPixels(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is used to generate and inspect HDR video")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe CLI is used to verify SDR metadata")
	}
	encoder, _ := exec.Command(ffmpeg, "-hide_banner", "-h", "encoder=libx264").Output()
	if !bytes.Contains(encoder, []byte("yuv420p10le")) {
		t.Skip("fixture generator needs 10-bit libx264")
	}
	for _, transfer := range []string{"smpte2084", "arib-std-b67"} {
		t.Run(transfer, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "hdr.mp4")
			out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i",
				"nullsrc=s=320x240:r=12:d=1,format=yuv420p10le,geq=lum=723:cb=512:cr=512",
				"-f", "lavfi", "-i", "sine=d=1", "-c:v", "libx264", "-preset", "ultrafast", "-qp", "0",
				"-x264-params", "colorprim=bt2020:transfer="+transfer+":colormatrix=bt2020nc",
				"-color_primaries", "bt2020",
				"-color_trc", transfer, "-colorspace", "bt2020nc", "-color_range", "tv",
				"-c:a", "aac", "-movflags", "+write_colr", source).CombinedOutput()
			if err != nil {
				t.Fatalf("fixture: %v\n%s", err, out)
			}
			path, err := Export(context.Background(), source, dir, "sdr", []timeline.Span{{End: 900000}}, false, discardProgress)
			if err != nil {
				t.Fatal(err)
			}
			out, err = exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
				"-show_entries", "stream=color_space,color_transfer,color_primaries", "-of", "csv=p=0", path).CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != "bt709,bt709,bt709" {
				t.Fatalf("SDR color metadata: %v\n%s", err, out)
			}
			pixels, err := exec.Command(ffmpeg, "-v", "error", "-i", path, "-map", "0:v:0",
				"-frames:v", "1", "-pix_fmt", "yuv420p", "-f", "rawvideo", "-").Output()
			const size = 320 * 240
			if err != nil || len(pixels) < size {
				t.Fatalf("decode SDR pixels: %v, %d bytes", err, len(pixels))
			}
			var sum int
			for _, pixel := range pixels[:size] {
				sum += int(pixel)
			}
			// A bit-depth-only conversion would leave the luma near 181.
			if mean := sum / size; mean >= 176 && mean <= 186 {
				t.Fatalf("luma still looks like quantized HDR: %d", mean)
			}
		})
	}
}

func TestFilteredVideoPreviewExcludesRemovedAudio(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is used to generate the video fixture")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mkv")
	const audio = "aevalsrc='sin(2*PI*440*t)*if(between(t,0.5,1.5),0.8,0.015)':s=48000:d=2"
	out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=24:d=2",
		"-f", "lavfi", "-i", audio, "-c:v", "mpeg4", "-c:a", "pcm_s16le", source).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	ctx := context.Background()
	spans := []timeline.Span{{End: 500000}, {Start: 1500000, End: 2000000}}
	path, err := Export(ctx, source, dir, "preview", spans, true, discardProgress)
	if err != nil {
		t.Fatal(err)
	}
	info, peaks, err := Waveform(ctx, path, discardProgress)
	if err != nil || !info.Video || math.Abs(float64(info.Duration-timeline.Second)) > 25000 {
		t.Fatalf("preview metadata %+v: %v", info, err)
	}
	var amplitude int
	for _, peak := range peaks {
		amplitude = max(amplitude, -int(peak.Min), int(peak.Max))
	}
	if amplitude < 200 || amplitude > 1600 {
		t.Fatalf("preview should contain only the quiet retained tones, got peak %d", amplitude)
	}
}

func TestVideoCut(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is used only to generate the test fixture")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe CLI is used only to independently check the result")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "varied-keyframes.mp4")
	output, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=2:size=320x180:rate=30",
		"-i", toneFixture(t), "-c:v", "mpeg4", "-g", "60", "-bf", "2", "-vf", "select='not(eq(mod(n,5),1))'",
		"-fps_mode", "vfr", "-c:a", "aac", source).CombinedOutput()
	if err != nil {
		t.Fatalf("creating fixture: %v %s", err, output)
	}
	spans := []timeline.Span{{Start: 342000, End: 713000}, {Start: 1123000, End: 1434000}}
	path, err := Export(context.Background(), source, dir, "cut", spans, false, discardProgress)
	if err != nil {
		t.Fatal(err)
	}
	output, err = exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,duration", "-of", "csv=p=0", path).CombinedOutput()
	if err != nil {
		t.Fatalf("checking result: %v %s", err, output)
	}
	lines := strings.Fields(string(output))
	for _, codec := range []string{"h264", "aac"} {
		found := false
		for _, line := range lines {
			name, raw, _ := strings.Cut(line, ",")
			if name == codec {
				value, err := strconv.ParseFloat(raw, 64)
				if err != nil || math.Abs(value-.682) > .034 {
					t.Fatalf("%s stream duration: %s (%v)", codec, raw, err)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("missing %s: %s", codec, output)
		}
	}
}

func TestH264ExportAndScaledPreview(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is used only to generate the test fixture")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe CLI is used only to independently check the result")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=2:size=1920x1080:rate=24",
		"-i", toneFixture(t), "-c:v", "libx264", "-preset", "ultrafast", "-g", "48", "-c:a", "aac", source).CombinedOutput()
	if strings.Contains(string(out), "Unknown encoder") {
		t.Skip("fixture generator needs libx264")
	}
	if err != nil {
		t.Fatalf("creating H.264 fixture: %v %s", err, out)
	}
	spans := []timeline.Span{{Start: 142000, End: 603000}, {Start: 930000, End: 1420000}}
	for _, test := range []struct {
		name, dimensions string
		edge             int
	}{
		{"native", "1920x1080", 0},
		{"proxy_720p", "1280x720", 1280},
		{"proxy_540p", "960x540", 960},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, err := Preview(context.Background(), source, dir, test.name, spans, test.edge, discardProgress)
			if err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
				"-show_entries", "stream=width,height,duration", "-of", "csv=p=0:s=x", path).CombinedOutput()
			if err != nil {
				t.Fatalf("checking video: %v %s", err, output)
			}
			parts := strings.Split(strings.TrimSpace(string(output)), "x")
			if len(parts) != 3 || strings.Join(parts[:2], "x") != test.dimensions {
				t.Fatalf("unexpected frame dimensions: %s", output)
			}
			if duration, parseErr := strconv.ParseFloat(parts[2], 64); parseErr != nil || math.Abs(duration-.951) > .04 {
				t.Fatalf("unexpected video duration: %s", output)
			}
			if out, err := exec.Command(ffmpeg, "-v", "error", "-xerror", "-i", path, "-f", "null", "-").CombinedOutput(); err != nil {
				t.Fatalf("decoding video: %v %s", err, out)
			}
		})
	}
}

func TestIndexedExportMatchesSequential(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is used to generate and compare the test fixture")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe CLI is used to check the output timestamps")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "indexed.mp4")
	// AAC perceptual noise substitution depends on the decoder's previous packets.
	// Disable it in the source fixture so indexed and sequential PCM can match exactly.
	out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=19:size=640x360:rate=24",
		"-f", "lavfi", "-i", "aevalsrc=0.3*sin(2*PI*(300*t+11*t*t)):s=48000:d=19",
		"-c:v", "libx264", "-preset", "fast", "-g", "24", "-bf", "2", "-c:a", "aac", "-aac_pns", "0", source).CombinedOutput()
	if strings.Contains(string(out), "Unknown encoder") {
		t.Skip("fixture generator needs libx264")
	}
	if err != nil {
		t.Fatalf("creating indexed fixture: %v %s", err, out)
	}
	spans := []timeline.Span{{Start: 5142000, End: 5763000}, {Start: 16473000, End: 17461000}}
	var results []string
	for _, skipGaps := range []bool{true, false} {
		last := -1.0
		started := time.Now()
		path, err := export(context.Background(), source, dir, "cut", spans, 0, false, skipGaps, func(value float64) {
			if value < last {
				t.Errorf("progress decreased: %f to %f", last, value)
			}
			last = value
		})
		if err != nil || last != 1 {
			t.Fatalf("export (skip=%t): %v, progress %f", skipGaps, err, last)
		}
		t.Logf("export (skip=%t): %s", skipGaps, time.Since(started).Round(time.Millisecond))
		results = append(results, path)
	}

	read := func(binary string, args ...string) []byte {
		t.Helper()
		out, err := exec.Command(binary, args...).Output()
		if err != nil {
			t.Fatalf("checking export with %s: %v", binary, err)
		}
		return out
	}
	packetTimes := func(path string) []byte {
		return read(ffprobe, "-v", "error", "-show_entries", "packet=stream_index,pts,duration", "-of", "csv=p=0", path)
	}
	if !bytes.Equal(packetTimes(results[0]), packetTimes(results[1])) {
		t.Error("seeking changed audio or video packet timestamps")
	}
	pcm := func(path string) []byte {
		return read(ffmpeg, "-v", "error", "-i", path, "-map", "0:a:0", "-f", "s16le", "-")
	}
	if !bytes.Equal(pcm(results[0]), pcm(results[1])) {
		t.Error("seeking changed decoded audio samples")
	}
	comparison, err := exec.Command(ffmpeg, "-hide_banner", "-nostats", "-i", results[0], "-i", results[1],
		"-lavfi", "ssim", "-an", "-f", "null", "-").CombinedOutput()
	_, raw, found := strings.Cut(string(comparison), "All:")
	if err != nil || !found {
		t.Fatalf("comparing decoded video: %v %s", err, comparison)
	}
	score, err := strconv.ParseFloat(strings.Fields(raw)[0], 64)
	if err != nil || score < .995 {
		t.Fatalf("seeking changed decoded video: SSIM %f (%v)", score, err)
	}

	t.Run("adjacent_preview_chunks", func(t *testing.T) {
		first := []timeline.Span{{Start: 11_301_000, End: 11_550_000}, {Start: 11_693_000, End: 12_000_000}}
		second := []timeline.Span{{Start: 12_000_000, End: 12_411_000}}
		for index, chunk := range [][]timeline.Span{first, second} {
			path, err := Preview(t.Context(), source, dir, "preview", chunk, 960, discardProgress)
			if err != nil {
				t.Fatal(err)
			}
			expected := []float64{.556, .411}[index]
			out := read(ffprobe, "-v", "error", "-show_entries", "stream=duration", "-of", "csv=p=0", path)
			durations := strings.Fields(string(out))
			if len(durations) != 2 {
				t.Fatalf("expected an audio and video stream: %s", out)
			}
			for _, raw := range durations {
				value, err := strconv.ParseFloat(raw, 64)
				if err != nil || math.Abs(value-expected) > .040 {
					t.Errorf("chunk %d stream duration %q, expected %.3fs", index, raw, expected)
				}
			}
			if samples := len(pcm(path)) / 2; math.Abs(float64(samples)-48_000*expected) > 1024 {
				t.Errorf("chunk %d has %d decoded audio samples, expected %.0f", index, samples, 48_000*expected)
			}
		}
	})
}

func BenchmarkLongVideoPreview(b *testing.B) {
	source := os.Getenv("SILENCECUT_TEST_VIDEO")
	if source == "" {
		b.Skip("set SILENCECUT_TEST_VIDEO to a long video with pauses")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		b.Fatal(err)
	}
	ctx := b.Context()
	info, peaks, err := Waveform(ctx, source, discardProgress)
	if err != nil {
		b.Fatal(err)
	}
	if !info.Video || info.Duration < 60*timeline.Second {
		b.Fatalf("expected at least a minute of video: %+v", info)
	}
	settings := timeline.Settings{Threshold: timeline.AutomaticThreshold(peaks), Before: 250000, After: 250000}
	clips, err := Detect(ctx, source, settings, discardProgress)
	if err != nil {
		b.Fatal(err)
	}
	spans := timeline.Kept(clips)
	var duration int64
	for _, span := range spans {
		duration += span.End - span.Start
	}
	if len(spans) < 2 || duration >= info.Duration {
		b.Fatalf("expected multiple retained parts, got %d", len(spans))
	}
	// Also time one late source-time window, as when starting at the playhead.
	windowStart := info.Duration / 24_000_000 * 12_000_000
	windowEnd := windowStart + 12_000_000
	var chunk []timeline.Span
	var chunkDuration int64
	for _, span := range spans {
		start, end := max(windowStart, span.Start), min(windowEnd, span.End)
		if start < end {
			chunk = append(chunk, timeline.Span{Start: start, End: end})
			chunkDuration += end - start
		}
	}
	type benchmarkCase struct {
		name, mode string
		edge       int
		spans      []timeline.Span
		duration   int64
	}
	cases := []benchmarkCase{
		{"auto_720p", "auto", 1280, spans, duration},
		{"auto_540p", "auto", 960, spans, duration},
		{"software_720p", "software", 1280, spans, duration},
		{"software_540p", "software", 960, spans, duration},
	}
	if len(chunk) > 0 {
		cases = append(cases,
			benchmarkCase{"auto_first_chunk", "auto", 960, chunk, chunkDuration},
			benchmarkCase{"software_first_chunk", "software", 960, chunk, chunkDuration})
	}
	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			b.Setenv("SILENCECUT_VIDEO_ACCELERATION", test.mode)
			dir := b.TempDir()
			for b.Loop() {
				path, err := Preview(b.Context(), source, dir, "preview", test.spans, test.edge, discardProgress)
				if err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				out, err := exec.Command(ffprobe, "-v", "error", "-show_entries",
					"format=duration", "-of", "csv=p=0", path).CombinedOutput()
				if err != nil {
					b.Fatalf("checking proxy duration: %v %s", err, out)
				}
				actual, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
				if err != nil || math.Abs(actual-float64(test.duration)/float64(timeline.Second)) > .1 {
					b.Fatalf("unexpected proxy duration %s for %d µs of retained media", out, test.duration)
				}
				if err := os.Remove(path); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
			b.ReportMetric(float64(info.Duration)/float64(timeline.Second), "source-s")
			b.ReportMetric(float64(test.duration)/float64(timeline.Second), "preview-s")
		})
	}
}
