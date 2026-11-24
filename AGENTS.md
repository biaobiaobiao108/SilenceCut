# SilenceCut 开发指南

## 项目范围

- SilenceCut 是 Go + Mygo 原生桌面应用，专注于从本地音视频中过滤静音、试听结果、微调区间并导出成片或切片。
- 支持 Windows amd64 和 macOS arm64。媒体处理使用随应用分发的 FFmpeg 库，运行时无需安装或调用 FFmpeg 命令行。
- 修改界面前先读 `DESIGN.md`。常用剪辑动作使用有可访问名称和悬停提示的图标，试听默认跳过移除段，可切到「原始」对照。

## 代码位置

- `main.go` 管理素材、预览和导出任务。界面与时间轴交互在 `view.go`、`waveform.go` 和 `icons.go`，音频试听在 `audio_preview.go`。
- `internal/timeline` 管理过滤区间、撤销和波形数据。区间使用源素材的微秒坐标，包含起点，排除终点。
- `internal/media` 通过 cgo 调用 FFmpeg，负责解码、静音检测、精确剪切和导出。视频优先使用 D3D11VA / Media Foundation 或 VideoToolbox，保留软件回退。导出使用目标目录内的临时文件，成功后再发布，避免覆盖同名结果。

## 构建与验证

- 使用 `go.mod` 指定的 Go 版本。Windows 使用 Git Bash、MinGW 和 NASM，macOS 使用 Xcode Command Line Tools。
- 首次运行 `sh scripts/build-ffmpeg.sh` 构建库，再运行 `sh scripts/package.sh` 打包。构建脚本固定 FFmpeg 版本并校验源码包。FFmpeg 工作目录默认是 `.ffmpeg/`，可通过 `SILENCECUT_FFMPEG_WORK` 指定。
- 本地验证时将 `CGO_CFLAGS` 和 `CGO_LDFLAGS` 指向 SDK 的 `include` 和 `lib`。Windows 还需将 SDK 的 `bin` 加入 `PATH`，并为 Go 测试和静态检查加上 `-tags mygo_nowebview`。macOS 使用默认构建标签。
- Go 改动用 `gofmt` 格式化，并运行 `go test ./...` 和 `go vet ./...`。视频集成测试使用本机 `ffmpeg` 生成测试素材，使用 `ffprobe` 独立验证导出结果。验证硬件路径时，可设置 `SILENCECUT_FFMPEG_DEBUG=1` 查看后端日志。
- `dist/` 和 `.ffmpeg/` 是生成产物。提交源码、测试、构建脚本和必要补丁。
- 每次修改完成并验证后，使用中文提交信息创建 Git 提交。
- 每次源码修改完成并验证后，除创建中文提交外，还要在当前平台运行 `sh scripts/package.sh` 重新打包；Windows 测试目录为 `dist/SilenceCut-windows-x64/`。
