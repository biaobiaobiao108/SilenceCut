package main

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

func TestFilterRejectsSourceChangeDuringAnalysis(t *testing.T) {
	path := selectionExportFixture(t)
	wav, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the two-second fixture with one second while it is being analyzed.
	wav = wav[:44+48000*2]
	binary.LittleEndian.PutUint32(wav[4:8], uint32(len(wav)-8))
	binary.LittleEndian.PutUint32(wav[40:44], uint32(len(wav)-44))
	item := asset{path: path}
	opts := filterOptions{preset: "中等"}
	replaced := false
	err = item.filter(t.Context(), opts, func(progress float64) {
		if progress >= .45 && !replaced {
			replaced = true
			if err := os.WriteFile(path, wav, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}, nil)
	if !replaced || err == nil || len(item.history.Clips) != 0 {
		t.Fatalf("source replaced = %t, filter error = %v, published cuts = %v", replaced, err, item.history.Clips)
	}
	if err := item.filter(t.Context(), opts, func(float64) {}, nil); err != nil {
		t.Fatal(err)
	}
	if item.info.Duration != timeline.Second || !validProjectClips(item.history.Clips, item.info.Duration) {
		t.Fatalf("retry returned duration %d and cuts %v", item.info.Duration, item.history.Clips)
	}
}

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

func TestProjectSaveAsReplacesExistingTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "已有项目.silencecut")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	source := &app{opts: filterOptions{preset: "慢"}}
	if err := source.saveProjectAs(path); err != nil {
		t.Fatal(err)
	}
	doc, err := readProject(path)
	if err != nil || doc.Preset != "慢" || source.projectPath != path || source.projectDirty {
		t.Fatalf("save as over existing target: %+v, %v, path=%s dirty=%t", doc, err, source.projectPath, source.projectDirty)
	}
	lock, err := lockProjectSave(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := source.saveProjectAs(path); !errors.Is(err, errProjectConflict) {
		t.Fatalf("save as while target is locked: %v", err)
	}
}

func TestProjectRejectsInvalidOptions(t *testing.T) {
	duration := 5 * timeline.Second
	clips := []timeline.Clip{{Span: timeline.Span{End: duration}, Keep: true}}
	item := &asset{
		path: "source.wav", name: "source.wav", info: media.Info{Duration: duration, Rate: 48000, Channels: 1},
		history: timeline.History{Clips: clips}, filtered: clips,
	}
	base := (&app{
		assets: []*asset{item}, selected: item,
		opts:     filterOptions{preset: "自定义", level: "-40", before: "0.1", after: "0.2"},
		position: timeline.Second, viewLength: 2 * timeline.Second,
		selection: timeline.Span{Start: timeline.Second, End: 2 * timeline.Second},
	}).snapshot("")
	tests := []struct {
		name string
		edit func(*projectDocument)
	}{
		{"unknown preset", func(doc *projectDocument) { doc.Preset = "未知" }},
		{"invalid custom value", func(doc *projectDocument) { doc.Before = "bad" }},
		{"selected out of range", func(doc *projectDocument) { doc.Selected = 2 }},
		{"view outside media", func(doc *projectDocument) { doc.ViewStart = duration }},
		{"selection outside media", func(doc *projectDocument) { doc.Selection.End = duration + 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := base
			doc.Assets = slices.Clone(base.Assets)
			tt.edit(&doc)
			path := filepath.Join(t.TempDir(), "invalid.silencecut")
			if err := writeProject(path, doc); err != nil {
				t.Fatal(err)
			}
			if _, err := readProject(path); err == nil {
				t.Fatal("invalid project was accepted")
			}
		})
	}
}

func TestProjectConcurrentSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "项目.silencecut")
	first, second := &app{opts: filterOptions{preset: "中等"}}, &app{opts: filterOptions{preset: "中等"}}
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

func TestSameDirectoryProjectsCanSaveIndependently(t *testing.T) {
	dir := t.TempDir()
	firstPath, secondPath := filepath.Join(dir, "first.silencecut"), filepath.Join(dir, "second.silencecut")
	first, second := &app{opts: filterOptions{preset: "中等"}}, &app{opts: filterOptions{preset: "中等"}}
	if err := first.saveProject(firstPath, true); err != nil {
		t.Fatal(err)
	}
	if err := second.saveProject(secondPath, true); err != nil {
		t.Fatal(err)
	}
	lock, err := lockProjectSave(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	if err := first.saveProject(firstPath, false); !errors.Is(err, errProjectConflict) {
		t.Fatalf("save while the same project is locked: %v", err)
	}
	if err := second.saveProject(secondPath, false); err != nil {
		t.Fatalf("save while a different project is locked: %v", err)
	}
}

func TestRecoverySkipsActiveSessions(t *testing.T) {
	dir := t.TempDir()
	first, previous, err := openRecoveryAt(dir)
	if err != nil || previous != nil {
		t.Fatalf("first launch: %v, %v", previous, err)
	}
	t.Cleanup(func() { first.close(false) })
	state := &app{recovery: first, projectDirty: true, projectPath: "existing.silencecut",
		opts: filterOptions{preset: "中等"}}
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

func TestRecoveryRemovesSupersededSessions(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	create := func(name string, modified time.Time) (string, string) {
		t.Helper()
		raw := filepath.Join(dir, name)
		snapshot := raw + projectExt
		if err := os.WriteFile(raw, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(snapshot, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{raw, snapshot} {
			if err := os.Chtimes(path, modified, modified); err != nil {
				t.Fatal(err)
			}
		}
		return raw, snapshot
	}
	oldRaw, oldSnapshot := create("session-old", now.Add(-2*time.Hour))
	newRaw, newSnapshot := create("session-new", now.Add(-time.Hour))
	orphan := filepath.Join(dir, "session-orphan")
	if err := os.WriteFile(orphan, nil, 0600); err != nil {
		t.Fatal(err)
	}

	current, previous, err := openRecoveryAt(dir)
	if err != nil || previous == nil || previous.file.Name() != newRaw {
		t.Fatalf("newest stale session was not selected: %v, %v", previous, err)
	}
	t.Cleanup(func() {
		current.close(true)
		previous.close(true)
	})
	for _, path := range []string{oldRaw, oldSnapshot, orphan} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("superseded recovery remains at %s: %v", path, err)
		}
	}
	for _, path := range []string{newRaw, newSnapshot} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("newest recovery was removed at %s: %v", path, err)
		}
	}
}

func TestCorruptRecoveryIsDiscarded(t *testing.T) {
	dir := t.TempDir()
	first, _, err := openRecoveryAt(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.close(true) })
	state := &app{recovery: first, projectDirty: true, opts: filterOptions{preset: "中等"}}
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
