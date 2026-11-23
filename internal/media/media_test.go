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

	"silencecut/internal/timeline"
)

func discardProgress(float64) {}

func toneFixture(t *testing.T) string {
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
		v := int16(0)
		if i >= rate/4 && i < rate*3/4 || i >= rate && i < rate*3/2 {
			v = int16(12000 * math.Sin(float64(i)*2*math.Pi*440/rate))
		}
		// One audible channel must keep a sample.
		_ = binary.Write(&wav, order, v)
		_ = binary.Write(&wav, order, int16(0))
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
		proxy            bool
	}{
		{"native", "1920x1080", false},
		{"proxy", "1280x720", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, err := Export(context.Background(), source, dir, test.name, spans, test.proxy, discardProgress)
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
