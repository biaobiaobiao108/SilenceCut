package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
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
	one, err := Export(ctx, path, dest, "结果", want, discardProgress)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Export(ctx, path, dest, "结果", want, discardProgress)
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
	_, err = Export(canceled, path, dest, "已取消", want, func(float64) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := Export(ctx, path, dest, "过短", []timeline.Span{{End: 1}}, discardProgress); err == nil {
		t.Fatal("sub-sample selection should fail before publishing a WAV")
	}
	files, err := filepath.Glob(filepath.Join(dest, "*"))
	if err != nil || !reflect.DeepEqual(files, []string{two, one}) {
		t.Fatalf("unexpected output directory: %v %v", files, err)
	}
}

func TestCancelWaveformAndPadding(t *testing.T) {
	source := toneFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, _, err := Waveform(ctx, source, func(float64) { cancel() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel analysis during decode: %v", err)
	}
	if _, _, err := Waveform(ctx, "missing.wav", discardProgress); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel analysis before opening a file: %v", err)
	}
	writer := audioWriter{ctx: ctx}
	if err := writer.silence(1_000_000); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel while padding audio: %v", err)
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

func TestPausePresetsRemoveBreathsAndProtectSpeech(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is used to create the PCM fixture")
	}
	const rate = 16000
	random := rand.New(rand.NewPCG(17, 39))
	var samples bytes.Buffer
	for i := range rate * 4 {
		ms := i * 1000 / rate
		phase := float64(i) * 2 * math.Pi * 175 / rate
		voice := math.Sin(phase) + .25*math.Sin(2*phase)
		noise := 2*random.Float64() - 1
		var sample float64
		switch {
		case ms >= 200 && ms < 700, ms >= 2175 && ms < 2600, ms >= 2700 && ms < 3100:
			sample = .16 * voice
		case ms >= 700 && ms < 800, ms >= 2600 && ms < 2700:
			sample = .035 * noise
		case ms >= 950 && ms < 1275, ms >= 3300 && ms < 3650:
			sample = .045 * noise
		case ms >= 1550 && ms < 2000:
			sample = .008 * voice
		}
		samples.Write(binary.LittleEndian.AppendUint16(nil, uint16(int16(sample*32767))))
	}
	source := filepath.Join(t.TempDir(), "voicing.wav")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "s16le", "-ar", "16000", "-ac", "1", "-i", "-", source)
	cmd.Stdin = &samples
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
	for _, preset := range []struct {
		pause, before, after int64
		margin               float64
		cutsShortPause       bool
	}{
		{280_000, 100_000, 120_000, 10, false},
		{200_000, 65_000, 80_000, 7, false},
		{140_000, 35_000, 50_000, 4, true},
	} {
		settings := timeline.Settings{Threshold: -36, MinPause: preset.pause, BreathMargin: preset.margin,
			Before: preset.before, After: preset.after}
		clips, err := Detect(context.Background(), source, settings, discardProgress)
		if err != nil {
			t.Fatal(err)
		}
		keeps := func(clips []timeline.Clip, at int64) bool {
			t.Helper()
			for _, clip := range clips {
				if clip.Start <= at && at < clip.End {
					return clip.Keep
				}
			}
			t.Fatalf("no clip for %d", at)
			return false
		}
		if keeps(clips, 1_100_000) || keeps(clips, 3_450_000) ||
			!keeps(clips, 720_000) || !keeps(clips, 1_750_000) || !keeps(clips, 2_650_000) ||
			keeps(clips, 2_090_000) == preset.cutsShortPause {
			t.Fatalf("unexpected speech/breath cuts at %d µs minimum: %v", preset.pause, clips)
		}
		settings.OnlyEnds = true
		ends, err := Detect(context.Background(), source, settings, discardProgress)
		if err != nil || !keeps(ends, 1_100_000) {
			t.Fatalf("ends-only mode removed an internal pause: %v, %v", ends, err)
		}
	}
}

func TestExportKeepsContinuousMatroskaAudio(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is used to generate Matroska fixtures")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "constant.mkv")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "aevalsrc=0.25:s=48000:d=2",
		"-c:a", "pcm_s16le", source).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v: %s", err, out)
	}
	result, err := Export(context.Background(), source, dir, "result", []timeline.Span{{End: 2 * timeline.Second}}, discardProgress)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{source, result} {
		pcm, err := exec.Command(ffmpeg, "-v", "error", "-i", path, "-f", "s16le", "-").Output()
		if err != nil || len(pcm) != 192_000 {
			t.Fatalf("%s: %d PCM bytes, %v", path, len(pcm), err)
		}
		for i := 0; i < len(pcm); i += 2 {
			if sample := binary.LittleEndian.Uint16(pcm[i:]); sample != 8192 {
				t.Fatalf("%s: sample %d = %d, want 8192", path, i/2, sample)
			}
		}
	}
}

func TestSDRExportPreservesColor(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg CLI is used to generate video fixtures")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe CLI is used to verify video color metadata")
	}
	for _, colorRange := range []string{"tv", "pc"} {
		t.Run(colorRange, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "bt709.mp4")
			if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=red:s=160x120:r=24:d=1",
				"-f", "lavfi", "-i", "sine=d=1", "-c:v", "libx264", "-pix_fmt", "yuv420p",
				"-colorspace", "bt709", "-color_trc", "bt709", "-color_primaries", "bt709", "-color_range", colorRange,
				"-c:a", "aac", source).CombinedOutput(); err != nil {
				t.Fatalf("fixture: %v: %s", err, out)
			}
			result, err := export(context.Background(), source, dir, "result", []timeline.Span{{End: timeline.Second}}, true, true, discardProgress)
			if err != nil {
				t.Fatal(err)
			}
			rgb := func(path string) []byte {
				t.Helper()
				fields, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
					"-show_entries", "stream=color_space,color_range", "-of", "default=nw=1", path).Output()
				if err != nil || !bytes.Contains(fields, []byte("color_space=bt709")) || !bytes.Contains(fields, []byte("color_range="+colorRange)) {
					t.Fatalf("%s: unexpected color metadata: %s, %v", path, fields, err)
				}
				pixels, err := exec.Command(ffmpeg, "-v", "error", "-i", path, "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-").Output()
				if err != nil || len(pixels) != 160*120*3 {
					t.Fatalf("%s: %d RGB bytes, %v", path, len(pixels), err)
				}
				center := (160*60 + 80) * 3
				return pixels[center : center+3]
			}
			before, after := rgb(source), rgb(result)
			for i := range before {
				if math.Abs(float64(before[i])-float64(after[i])) > 8 {
					t.Fatalf("source RGB %v, export RGB %v", before, after)
				}
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
			path, err := Export(context.Background(), source, dir, "sdr", []timeline.Span{{End: 900000}}, discardProgress)
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

func TestVideoExportExcludesRemovedAudio(t *testing.T) {
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
	path, err := Export(ctx, source, dir, "cut", spans, discardProgress)
	if err != nil {
		t.Fatal(err)
	}
	info, peaks, err := Waveform(ctx, path, discardProgress)
	if err != nil || !info.Video || math.Abs(float64(info.Duration-timeline.Second)) > 25000 {
		t.Fatalf("export metadata %+v: %v", info, err)
	}
	var amplitude int
	for _, peak := range peaks {
		amplitude = max(amplitude, -int(peak.Min), int(peak.Max))
	}
	if amplitude < 200 || amplitude > 1600 {
		t.Fatalf("export should contain only the quiet retained tones, got peak %d", amplitude)
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
	for _, test := range []struct {
		name  string
		spans []timeline.Span
		want  float64
	}{
		{"joined", []timeline.Span{{Start: 342000, End: 713000}, {Start: 1123000, End: 1434000}}, .682},
		{"partial frame", []timeline.Span{{Start: 500000, End: 510000}}, .010},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, err := Export(t.Context(), source, dir, "cut", test.spans, discardProgress)
			if err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,duration", "-of", "csv=p=0", path).CombinedOutput()
			if err != nil {
				t.Fatalf("checking result: %v %s", err, output)
			}
			lines := strings.Fields(string(output))
			for _, codec := range []string{"h264", "aac"} {
				found := false
				for _, line := range lines {
					name, raw, _ := strings.Cut(line, ",")
					if name != codec {
						continue
					}
					value, err := strconv.ParseFloat(raw, 64)
					if err != nil || math.Abs(value-test.want) > .001 {
						t.Fatalf("%s stream duration: %s, want %.3f (%v)", codec, raw, test.want, err)
					}
					found = true
				}
				if !found {
					t.Fatalf("missing %s: %s", codec, output)
				}
			}
		})
	}
	t.Run("sub-millisecond AAC", func(t *testing.T) {
		spans := []timeline.Span{{Start: 500_000, End: 500_400}}
		path, err := Export(t.Context(), source, dir, "short", spans, discardProgress)
		if err != nil {
			t.Fatal(err)
		}
		pcm, err := exec.Command(ffmpeg, "-v", "error", "-i", path, "-map", "0:a:0", "-f", "s16le", "-").Output()
		if err != nil {
			t.Fatalf("decoding audio: %v", err)
		}
		// 400 microseconds round to 19 stereo samples at 48 kHz.
		if len(pcm) != 19*2*2 {
			t.Fatalf("decoded %d PCM bytes, want 76", len(pcm))
		}
	})
}

func TestH264ExportKeepsSourceSize(t *testing.T) {
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
	path, err := Export(t.Context(), source, dir, "cut", spans, discardProgress)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,duration", "-of", "csv=p=0:s=x", path).CombinedOutput()
	if err != nil {
		t.Fatalf("checking video: %v %s", err, output)
	}
	parts := strings.Split(strings.TrimSpace(string(output)), "x")
	if len(parts) != 3 || strings.Join(parts[:2], "x") != "1920x1080" {
		t.Fatalf("unexpected frame dimensions: %s", output)
	}
	if duration, parseErr := strconv.ParseFloat(parts[2], 64); parseErr != nil || math.Abs(duration-.951) > .04 {
		t.Fatalf("unexpected video duration: %s", output)
	}
	if out, err := exec.Command(ffmpeg, "-v", "error", "-xerror", "-i", path, "-f", "null", "-").CombinedOutput(); err != nil {
		t.Fatalf("decoding video: %v %s", err, out)
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
		path, err := export(context.Background(), source, dir, "cut", spans, false, skipGaps, func(value float64) {
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

}

func BenchmarkLongVideoExport(b *testing.B) {
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
	for _, mode := range []string{"auto", "software"} {
		b.Run(mode, func(b *testing.B) {
			b.Setenv("SILENCECUT_VIDEO_ACCELERATION", mode)
			dir := b.TempDir()
			for b.Loop() {
				path, err := Export(b.Context(), source, dir, "cut", spans, discardProgress)
				if err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				out, err := exec.Command(ffprobe, "-v", "error", "-show_entries",
					"format=duration", "-of", "csv=p=0", path).CombinedOutput()
				if err != nil {
					b.Fatalf("checking export duration: %v %s", err, out)
				}
				actual, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
				if err != nil || math.Abs(actual-float64(duration)/float64(timeline.Second)) > .1 {
					b.Fatalf("unexpected export duration %s for %d µs of retained media", out, duration)
				}
				if err := os.Remove(path); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
			}
			b.ReportMetric(float64(info.Duration)/float64(timeline.Second), "source-s")
			b.ReportMetric(float64(duration)/float64(timeline.Second), "output-s")
		})
	}
}
