# 极速过滤

将口播或对白中的静音自动标记为移除区间。拖入音视频后会自动过滤。

- 主波形默认显示 30 秒，可用滑杆和加减按钮缩放。点击下方全局波形定位，拖动框出的区域浏览其他时间段。
- 点击主波形定位，拖动选择区间。用「保留」或「移除」修改选区或游标所在片段。拖动波形上沿的短柄可调整片段边界，改动可撤销。
- 播放键或空格默认试听「过滤后」的结果，跳过移除段。点「原始」可试听完整素材。图标的操作名称可通过悬停提示查看。

- **导出合并媒体**为每个有保留区间的素材生成一个文件。
- **导出切片**将区间分别写入素材的切片文件夹。勾选「切片仅导出保留段」可省略移除区间。
- 视频导出为 H.264/AAC MP4，音频导出为 24 位 PCM WAV。选用第一条音轨，视频输出最多保留两个声道。过滤、预览与导出都在本机运行。
- Windows 10/11 amd64 解压后双击 `SilenceCut.exe`。Windows N 版需要系统的 Media Feature Pack。
- Apple Silicon（arm64）macOS 14+ 解压后运行 `SilenceCut.app`。本地构建包使用临时签名，公开分发需使用开发者签名和公证。
- 视频导出优先使用 D3D11VA / Media Foundation（Windows）或 VideoToolbox（macOS）的硬件编解码。兼容帧直接交给编码器，支持时在设备上缩放。驱动或编码配置不支持时自动回退。音频解码、静音检测、分析波形和音频导出仍由 CPU 处理。

## 从源码构建

需要 Go 1.27.4。Windows 使用带 GCC、GNU Make 和标准 Unix 命令的 Git Bash / MinGW 环境。macOS 使用 Xcode Command Line Tools。脚本固定 FFmpeg 8.1.3 并核对源码包 SHA-256。

Windows，在 Git Bash 中运行

```sh
sh scripts/build-ffmpeg.sh
sh scripts/package.sh
```

macOS，在终端运行

```sh
sh scripts/build-ffmpeg.sh
sh scripts/package.sh
```

发布包位于 `dist/`。编译 Go 时通过 `CGO_CFLAGS` 和 `CGO_LDFLAGS` 指向对应架构 SDK 的 `include` 和 `lib`。运行 Windows 测试时还需将 SDK 的 `bin` 加入 PATH。应用运行不需要系统安装 FFmpeg。

## 参考行为

AutoCutVideo 2.33.5 的 `/remove/silence` 路由连接 Go 的 `client/remove/silence`，采用 0.1 秒的 FFmpeg 静音检测。原版的三个预设使用波形推算阈值，并对检测片段加前后留白。静态依据来自 REA 的 Rust/Go 检查、内嵌网页资源及 Go 符号与反汇编。波形编辑和导出界面独立实现。
