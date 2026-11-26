package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo"

	"silencecut/internal/media"
	"silencecut/internal/timeline"
)

func assetErrorText(item *asset) string {
	if item == nil {
		return ""
	}
	var details []string
	if item.issue != "" && !item.pendingLoad {
		details = append(details, "处理失败 · "+item.issue)
	}
	if item.exportIssue != "" {
		details = append(details, "导出失败 · "+item.exportIssue)
	}
	return strings.Join(details, "\n")
}

func (a *app) errorReport() (int, string) {
	var count int
	var details []string
	for _, item := range a.assets {
		detail := assetErrorText(item)
		if detail == "" {
			continue
		}
		count++
		if item.path != "" {
			detail = "路径 · " + item.path + "\n" + detail
		}
		details = append(details, item.name+"\n"+detail)
	}
	return count, strings.Join(details, "\n\n")
}

func (a *app) showErrorDetails(message, detail string) {
	if a.busy() || detail == "" {
		return
	}
	a.picking = true
	a.async(func() {
		result, err := mygo.Dialog.Message(mygo.MessageOptions{
			Parent: a.win, Type: mygo.MessageError, Title: title, Message: message, Detail: detail,
			Buttons: []string{"复制错误", "关闭"}, DefaultButton: 1, CancelButton: 1,
		})
		a.win.Update(func() {
			a.picking = false
			if a.closing {
				return
			}
			if err != nil {
				a.status = err.Error()
			} else if result.Button == 0 {
				mygo.Clipboard.WriteText(detail)
				a.status = "已复制错误详情"
			}
		})
	})
}

func (a *app) showAssetError(item *asset) {
	if detail := assetErrorText(item); detail != "" {
		if item.path != "" {
			detail = "路径 · " + item.path + "\n" + detail
		}
		a.showErrorDetails(item.name, detail)
	}
}

func (a *app) showAllAssetErrors() {
	count, detail := a.errorReport()
	if count > 0 {
		a.showErrorDetails(fmt.Sprintf("%d 个素材需要检查", count), detail)
	}
}

func (a *app) chooseExport() {
	if a.busy() {
		return
	}
	count := 0
	for _, item := range a.assets {
		if a.exportable(item) {
			count++
		}
	}
	if count == 0 {
		return
	}
	a.pick(mygo.OpenDialogOptions{
		Parent: a.win, Title: fmt.Sprintf("为 %d 个素材选择导出文件夹", count), DefaultPath: a.outputDir,
		Directory: true, CreateDirectories: true, ButtonLabel: fmt.Sprintf("导出 %d 个素材", count),
	}, func(paths []string) { a.export(paths[0]) })
}

func (a *app) canExport() bool {
	return slices.ContainsFunc(a.assets, func(item *asset) bool { return a.exportable(item) })
}

func (a *app) exportable(item *asset) bool {
	return item.issue == "" && slices.ContainsFunc(item.history.Clips, func(clip timeline.Clip) bool { return clip.Keep })
}

func (a *app) export(dir string) {
	a.outputDir = dir
	type exportWork struct {
		item     *asset
		snapshot asset
	}
	type exportFailure struct {
		item  *asset
		issue string
	}
	var work []exportWork
	for _, item := range a.assets {
		if a.exportable(item) {
			item.exportIssue = ""
			work = append(work, exportWork{item: item, snapshot: *item})
		}
	}
	ctx := a.begin("导出")
	a.async(func() {
		report := a.report()
		var count, failures int
		var failed []exportFailure
		for index, item := range work {
			if ctx.Err() != nil {
				break
			}
			var n int
			current, err := os.Stat(item.snapshot.path)
			if err == nil && changed(item.snapshot.file, current) {
				err = errors.New("素材已改变，请先重新过滤")
			}
			if err == nil {
				update := func(value float64) { report((float64(index) + value) / float64(len(work))) }
				n, err = exportAsset(ctx, item.snapshot, dir, update)
			}
			count += n
			if err != nil && !errors.Is(err, context.Canceled) {
				failures++
				failed = append(failed, exportFailure{item: item.item, issue: err.Error()})
			}
		}
		detail := "可查看错误详情"
		if failures == 0 {
			detail = ""
		}
		status := batchStatus("导出", fmt.Sprintf("已导出 %d 个文件", count), count, failures, detail, ctx.Err() != nil)
		a.win.Update(func() {
			if a.closing {
				return
			}
			for _, item := range failed {
				item.item.exportIssue = item.issue
			}
			if count > 0 {
				a.publishedDir = dir
			}
			a.end(status)
		})
	})
}

func exportAsset(ctx context.Context, item asset, dir string, progress media.Progress) (int, error) {
	base := strings.TrimSuffix(item.name, filepath.Ext(item.name))
	spans := timeline.Kept(item.history.Clips)
	if len(spans) == 0 {
		return 0, nil
	}
	_, err := media.Export(ctx, item.path, dir, base+"_已过滤", spans, progress)
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func (a *app) timelineSelection(item *asset) (timeline.Span, bool) {
	if item == nil {
		return timeline.Span{}, false
	}
	span := a.selection
	return span, span.Start >= 0 && span.End > span.Start && span.End <= item.info.Duration
}

func (a *app) chooseSelectionExport() {
	item := a.selected
	span, ok := a.timelineSelection(item)
	if a.busy() || item == nil || item.offline || item.issue != "" || !ok {
		return
	}
	a.pick(mygo.OpenDialogOptions{
		Parent: a.win, Title: "导出选中区域", DefaultPath: a.outputDir, Directory: true,
		CreateDirectories: true, ButtonLabel: "导出选中区域",
	}, func(paths []string) { a.exportSelection(item, span, paths[0]) })
}

func (a *app) exportSelection(item *asset, span timeline.Span, dir string) {
	if a.busy() || item == nil {
		return
	}
	a.outputDir = dir
	item.exportIssue = ""
	snapshot := *item
	ctx := a.begin("导出选中区域")
	a.async(func() {
		path, err := exportSelectionAsset(ctx, snapshot, span, dir, a.report())
		status := "已导出选中区域 · " + filepath.Base(path)
		switch {
		case errors.Is(err, context.Canceled):
			status = "已取消导出选中区域"
		case err != nil:
			status = "导出选中区域失败 · " + err.Error()
		}
		a.win.Update(func() {
			if a.closing {
				return
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				item.exportIssue = err.Error()
			}
			if err == nil {
				a.publishedDir = dir
			}
			a.end(status)
		})
	})
}

func exportSelectionAsset(ctx context.Context, item asset, span timeline.Span, dir string, progress media.Progress) (string, error) {
	if span.Start < 0 || span.End <= span.Start || item.info.Duration <= 0 || span.End > item.info.Duration {
		return "", errors.New("选区超出素材范围")
	}
	current, err := os.Stat(item.path)
	if err != nil {
		return "", err
	}
	if changed(item.file, current) {
		return "", errors.New("素材已改变，请先重新过滤")
	}
	base := strings.TrimSuffix(item.name, filepath.Ext(item.name))
	return media.Export(ctx, item.path, dir, base+"_选中区域", []timeline.Span{span}, progress)
}
