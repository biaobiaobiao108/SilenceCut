# 极速过滤

将口播或对白中的静音自动标记为移除区间。拖入音视频后会自动过滤。

- 主波形默认显示 30 秒，可用滑杆和加减按钮缩放。点击下方全局波形定位，拖动框出的区域浏览其他时间段。
- 直接拖动播放游标的顶部手柄或竖线，按 Escape 可恢复拖动前的位置。点击波形空白处定位，从空白处拖动可框选区间。用「保留」或「移除」修改选区或游标所在片段。拖动波形上沿的边界手柄可调整片段，改动可撤销。
- 播放键或空格默认试听「过滤后」的结果，跳过移除段。视频首次试听或区间修改后需要准备临时预览。点「原始」可试听完整素材。图标的操作名称可通过悬停提示查看。
- 音频素材在编辑区直接显示大波形，视频素材在波形上方显示画面。可从工作区任意位置拖入本地素材。
- 窗口较窄时，从顶部「过滤设置」打开参数面板。

- **导出合并媒体**为每个有保留区间的素材生成一个文件。
- **导出切片**将区间分别写入素材的切片文件夹。勾选「切片仅导出保留段」可省略移除区间。
- 视频导出为 H.264/AAC MP4，HDR 素材会进行色调映射并输出为 SDR。音频导出为 24 位 PCM WAV。选用第一条音轨，视频输出最多保留两个声道。过滤、预览与导出都在本机运行。
- Windows 10/11 amd64 运行 `SilenceCut-windows-x64-setup.exe`，安装后从开始菜单打开 SilenceCut。Windows N 版需要系统的 Media Feature Pack。
- Apple Silicon（arm64）macOS 14+ 打开 `SilenceCut-macos-arm64.pkg`，安装后从「应用程序」打开 SilenceCut。当前安装包未签名和公证，安装时可能需要在「系统设置 → 隐私与安全性」中手动允许。
- 视频导出和预览转码默认启用硬件加速。Windows 使用 D3D11VA 和 Media Foundation，macOS 使用 VideoToolbox。兼容的帧直接交给编码器，支持时在设备上缩放。驱动或编码配置不支持时自动回退到可用路径。
- MP4 和 MOV 导出遇到较长的移除段时，会在索引可用的情况下跳到下一段附近的关键帧，再精确处理切点。定位后无法读到完整区间时，自动改用顺序解码。音频解码、静音检测、波形分析和音频导出由 CPU 处理。

## 从源码构建

需要 Go 1.27.4。Windows 使用带 GCC、GNU Make 和标准 Unix 命令的 Git Bash / MinGW 环境，生成安装包还需要 Inno Setup 6。自定义安装位置可通过 `ISCC` 指定编译器 `ISCC.exe` 的路径。macOS 使用 Xcode Command Line Tools。脚本固定 FFmpeg 8.1.3 并核对源码包 SHA-256。

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

发布包位于 `dist/`。窗口与发布包图标均来自 `assets/silencecut.svg`。编译 Go 时通过 `CGO_CFLAGS` 和 `CGO_LDFLAGS` 指向对应架构 SDK 的 `include` 和 `lib`。运行 Windows 测试时还需将 SDK 的 `bin` 加入 PATH。应用运行不需要系统安装 FFmpeg。

排查视频导出或预览转码时，可在启动应用前设置 `SILENCECUT_VIDEO_ACCELERATION=software`，让这两项任务走系统的软件编解码路径。设置 `SILENCECUT_FFMPEG_DEBUG=1` 可在标准错误中查看帧传输方式、回退原因、定位次数和解码帧数。

## 参考行为

AutoCutVideo 2.33.5 的 `/remove/silence` 路由连接 Go 的 `client/remove/silence`，采用 0.1 秒的 FFmpeg 静音检测。原版的三个预设使用波形推算阈值，并对检测片段加前后留白。静态依据来自 REA 的 Rust/Go 检查、内嵌网页资源及 Go 符号与反汇编。波形编辑和导出界面独立实现。
