package main

import (
	"context"
	"errors"
	"os"
	"slices"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

// A filtered video contains only retained media. The editor still uses source time.
type filteredVideo struct {
	path  string
	spans []timeline.Span
}

func (v *filteredVideo) editedPosition(source int64) int64 {
	var edited int64
	for _, span := range v.spans {
		if source < span.End {
			return edited + max(0, source-span.Start)
		}
		edited += span.End - span.Start
	}
	return edited
}

func (v *filteredVideo) sourcePosition(edited int64) int64 {
	for _, span := range v.spans {
		length := span.End - span.Start
		if edited < length {
			return span.Start + max(0, edited)
		}
		edited -= length
	}
	return v.spans[len(v.spans)-1].End
}

func (a *app) prepareFilteredVideo(spans []timeline.Span) {
	item := a.selected
	if item.cutPreview != nil && slices.Equal(item.cutPreview.spans, spans) {
		a.openFilteredVideo(item.cutPreview)
		return
	}
	source, file := item.path, item.file
	seq, dir := a.previewSeq, a.temp
	ctx, cancel := context.WithCancel(context.Background())
	a.previewCancel, a.buildingCut = cancel, true
	a.async(func() {
		var path string
		stat, err := os.Stat(source)
		if err == nil && changed(file, stat) {
			err = errors.New("素材已改变，请先重新过滤")
		}
		if err == nil {
			path, err = media.Export(ctx, source, dir, "filtered-preview", spans, true, func(float64) {})
		}
		a.win.Update(func() {
			if a.closing || seq != a.previewSeq || ctx.Err() != nil {
				if err == nil {
					_ = os.Remove(path)
				}
				return
			}
			cancel()
			a.previewCancel, a.buildingCut = nil, false
			if err != nil {
				a.status = "无法准备过滤后试听 · " + err.Error()
				return
			}
			if item.cutPreview != nil {
				_ = os.Remove(item.cutPreview.path)
			}
			item.cutPreview = &filteredVideo{path: path, spans: spans}
			a.openFilteredVideo(item.cutPreview)
		})
	})
}

func (a *app) openFilteredVideo(cut *filteredVideo) {
	a.closePreview()
	if err := a.openVideo(cut.path); err != nil {
		a.filteredVideoFailed(err.Error())
		return
	}
	a.videoCut, a.previewPlay = cut, true
	a.seek(a.position)
}

func (a *app) filteredVideoFailed(issue string) {
	item := a.selected
	a.closePreview()
	_ = os.Remove(item.cutPreview.path)
	item.cutPreview = nil
	a.openPreview(item, false)
	a.status = "无法播放过滤后试听 · " + issue
}
