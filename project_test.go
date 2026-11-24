package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

func TestCloseWaitsForActiveJobBeforePrompting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &app{projectDirty: true, cancel: cancel}
	if state.requestClose() || !state.closePending || ctx.Err() != context.Canceled {
		t.Fatal("closing a dirty project must wait for the canceled job")
	}
}

func TestProjectRoundTrip(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "源素材.wav")
	if err := os.WriteFile(source, []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	baseline := []timeline.Clip{{Span: timeline.Span{End: 6_000_003}, Keep: true}}
	item := &asset{path: source, file: file, info: media.Info{Duration: baseline[0].End, Rate: 48000, Channels: 1},
		filtered: baseline, history: timeline.History{Clips: baseline}}
	a := &app{assets: []*asset{item}, selected: item,
		opts:     filterOptions{preset: "自定义", level: "-38.5", before: "0.25", after: "0.33", ends: true},
		position: 2_000_009, viewLength: 3_000_011, viewStart: 1_000_005,
		selection: timeline.Span{Start: 1_000_006, End: 2_000_007}}
	path := filepath.Join(dir, "项目.silencecut")
	if err := writeProject(path, a.snapshot(path)); err != nil {
		t.Fatal(err)
	}
	item.history.Edit(timeline.Span{Start: 1_015_027, End: 2_031_049}, false)
	if err := writeProject(path, a.snapshot(path)); err != nil {
		t.Fatal(err)
	}
	doc, err := readProject(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := doc.Assets[0]
	if saved.Path != filepath.Base(source) || saved.Source != stampOf(file) || !slices.Equal(saved.Filtered, baseline) ||
		!slices.Equal(saved.Clips, item.history.Clips) || doc.Selected != 0 || doc.Position != a.position ||
		doc.ViewStart != a.viewStart || doc.ViewLength != a.viewLength || doc.Selection != a.selection ||
		!doc.Ends || doc.Level != a.opts.level {
		t.Fatalf("project round trip did not preserve the draft: %+v %+v", doc, saved)
	}
	doc.Assets[0].Clips[0].End--
	if err := writeProject(path, doc); err != nil {
		t.Fatal(err)
	}
	if _, err := readProject(path); err == nil {
		t.Fatal("a gap in the saved timeline must be rejected")
	}
}

func TestProjectSaveRejectsStaleVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "项目.silencecut")
	first := &app{opts: filterOptions{preset: "慢"}}
	if err := first.saveProject(path, true); err != nil {
		t.Fatal(err)
	}
	stale := &app{}
	stale.openProject(path)
	first.opts.preset = "快"
	first.markProject()
	if err := first.saveProject(path, false); err != nil {
		t.Fatal(err)
	}
	stale.opts.ends = true
	stale.markProject()
	if err := stale.saveProject(path, false); !errors.Is(err, errProjectConflict) || !stale.projectDirty {
		t.Fatalf("stale save = %v, dirty = %t", err, stale.projectDirty)
	}
	copyPath := filepath.Join(filepath.Dir(path), "副本.silencecut")
	if err := stale.saveProject(copyPath, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, preset string
		ends         bool
	}{{path, "快", false}, {copyPath, "慢", true}} {
		doc, err := readProject(tc.path)
		if err != nil || doc.Preset != tc.preset || doc.Ends != tc.ends {
			t.Fatalf("read %s: %+v, %v", tc.path, doc, err)
		}
	}
}

func TestProjectConcurrentSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "项目.silencecut")
	first, second := &app{}, &app{}
	if err := first.saveProject(path, true); err != nil {
		t.Fatal(err)
	}
	second.openProject(path)
	first.opts.preset, second.opts.preset = "慢", "快"
	first.markProject()
	second.markProject()
	start := make(chan struct{})
	var workers sync.WaitGroup
	results := make(chan error, 2)
	for _, editor := range []*app{first, second} {
		workers.Go(func() {
			<-start
			results <- editor.saveProject(path, false)
		})
	}
	close(start)
	workers.Wait()
	close(results)
	saved, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			saved++
		case errors.Is(err, errProjectConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if saved != 1 || conflicts != 1 {
		t.Fatalf("concurrent saves: %d committed, %d conflicts", saved, conflicts)
	}
}

func TestRecoverySkipsActiveSessions(t *testing.T) {
	dir := t.TempDir()
	first, previous, err := openRecoveryAt(dir)
	if err != nil || previous != nil {
		t.Fatalf("first launch: %v, %v", previous, err)
	}
	t.Cleanup(func() { first.close(false) })
	state := &app{recovery: first, projectDirty: true, projectPath: "existing.silencecut"}
	if err := state.writeRecovery(); err != nil {
		t.Fatal(err)
	}
	second, previous, err := openRecoveryAt(dir)
	if err != nil || previous != nil {
		t.Fatalf("active snapshot must stay private to the owner: %v, %v", previous, err)
	}
	second.close(true)
	first.close(false)
	third, previous, err := openRecoveryAt(dir)
	if err != nil || previous == nil {
		t.Fatalf("stale snapshot should be available: %v, %v", previous, err)
	}
	defer third.close(true)
	defer previous.close(true)
	doc, err := readProject(previous.file.Name() + projectExt)
	if err != nil || doc.Project != state.projectPath {
		t.Fatalf("stale snapshot was not readable: %+v, %v", doc, err)
	}
}

func TestCorruptRecoveryIsDiscarded(t *testing.T) {
	dir := t.TempDir()
	first, _, err := openRecoveryAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.close(true) })
	state := &app{recovery: first, projectDirty: true}
	if err := state.writeRecovery(); err != nil {
		t.Fatal(err)
	}
	snapshot := first.file.Name() + projectExt
	first.close(false)
	if err := os.WriteFile(snapshot, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}

	current, previous, err := openRecoveryAt(dir)
	if err != nil || previous == nil {
		t.Fatalf("open corrupt recovery: %v, %v", current, previous)
	}
	t.Cleanup(func() { current.close(true) })
	state.offerRecovery(previous)
	if !strings.Contains(state.status, "恢复快照已损坏并已丢弃") {
		t.Fatalf("status = %q", state.status)
	}
	for _, path := range []string{snapshot, strings.TrimSuffix(snapshot, projectExt)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("corrupt recovery remains at %s: %v", path, err)
		}
	}
}
