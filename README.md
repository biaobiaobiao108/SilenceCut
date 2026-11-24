# 极速过滤

将口播或对白中的停顿和气口自动标记为移除区间。拖入音视频后会自动过滤。

- 默认使用「中等」。「慢」保留较短的自然停顿和更多句间留白，「快」会缩短留白，也会处理更短、更明显的气口。
- 三档预设会参考素材自身的音量和周期性，优先保留有声对白。贴着字音的气口和接近对白音量的呼吸声仍可能保留，可试听后调整。「自定义」按指定音量阈值和留白检测静音。

- 主波形默认显示 30 秒，可用滑杆和加减按钮缩放。点击下方全局波形定位，拖动框出的区域浏览其他时间段。
- 素材行和主波形支持右键、macOS Control-click、菜单键和 Shift+F10。右键素材行会先选中该素材；右键主波形会把游标移到点击位置并保留当前选区。
- 直接拖动播放游标的顶部手柄或竖线，按 Escape 可恢复拖动前的位置。点击波形空白处定位，从空白处拖动可框选区间。用「保留」或「移除」修改选区或游标所在片段。拖动波形上沿的边界手柄可调整片段，改动可撤销。聚焦波形后，Alt+左右方向键可微调当前片段起点，Alt+Shift+左右方向键可微调终点。
- 播放键或空格默认试听「过滤后」的结果，跳过移除段。音频用内置 FFmpeg 流式解码，环绕声音轨以立体声试听。视频直接用系统播放器播放原片，在切点跳转，密集切点处可能出现短暂停顿。点「原始」可连续播放完整素材。
- 音频素材在编辑区直接显示大波形，视频素材在波形上方显示画面。可从工作区任意位置拖入本地素材。
- 窗口较窄时，从顶部「过滤设置」打开参数面板。

## 项目

- 「保存项目」或 Ctrl/Cmd+S 将素材路径、自动和手工切点、过滤选项、当前游标及缩放保存为 `.silencecut`。可从顶部「打开项目」、Ctrl/Cmd+Shift+O 或拖入项目文件继续编辑。
- 项目引用原始文件。移动项目时，可连同素材一起移动并保留相对目录结构。找不到素材或素材发生变化时，选中该素材并点击「重新定位…」。替换文件须与原记录的素材类型和时长一致，才能继续使用已保存的切点。
- 取消读取项目素材后，可通过「继续读取素材」沿用已存切点加载剩余文件。
- 编辑时自动创建本机恢复快照。异常退出后，下次直接启动应用时可选择恢复上一次尚未保存的编辑。

## 导出与安装

- **导出合并媒体**为每个有保留区间的素材生成一个文件，开始前目录选择器会显示本批次处理的素材数量。
- 在波形框选后，可从右键菜单选择「导出选中区域…」。它只导出该连续区间，不受保留/移除状态影响；文件名为 `<素材名>_选中区域.mp4/wav`，同名文件自动递增为 `(2)`。
- 视频导出为 H.264/AAC MP4，HDR 素材会进行色调映射并输出为 SDR。音频导出为 24 位 PCM WAV。选用第一条音轨，视频输出最多保留两个声道。过滤、预览与导出都在本机运行。
- Windows 10/11 amd64 运行 `SilenceCut-windows-x64-setup.exe`，安装后从开始菜单打开 SilenceCut。当前安装包未签名。Windows N 版需要系统的 Media Feature Pack。
- Apple Silicon（arm64）macOS 14+ 打开 `SilenceCut-macos-arm64.pkg`，安装后从「应用程序」打开 SilenceCut。当前安装包未签名和公证，安装时可能需要在「系统设置 → 隐私与安全性」中手动允许。
- 视频导出默认启用硬件加速。Windows 使用 D3D11VA 和 Media Foundation，macOS 使用 VideoToolbox。兼容的帧直接交给编码器。驱动或编码配置不支持时自动回退到可用路径。
- MP4 和 MOV 导出遇到较长的移除段时，会在索引可用的情况下跳到下一段附近的关键帧，再精确处理切点。定位后无法读到完整区间时，自动改用顺序解码。音频解码、静音检测、波形分析和音频导出由 CPU 处理。

## 从源码构建

需要 Go 1.27.4。Windows 使用带 GCC、GNU Make、NASM 和标准 Unix 命令的 Git Bash / MinGW 环境，生成安装包还需要 Inno Setup 6。自定义安装位置可通过 `ISCC` 指定编译器 `ISCC.exe` 的路径。macOS 使用 Xcode Command Line Tools。脚本固定 FFmpeg 8.1.3 并核对源码包 SHA-256。

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

排查视频导出时，可在启动应用前设置 `SILENCECUT_VIDEO_ACCELERATION=software`，使用软件编解码路径。设置 `SILENCECUT_FFMPEG_DEBUG=1` 可在标准错误中查看帧传输方式、回退原因、定位次数和解码帧数。

## 验证

设置 `SILENCECUT_TEST_VIDEO` 指向一段至少一分钟、带有停顿的本地视频后，可用下面的命令比较默认和强制软件的视频导出路径。`ffprobe` 会核对导出文件的时长。Windows 需要加上 `-tags mygo_nowebview`。

```sh
go test -run '^$' -bench '^BenchmarkLongVideoExport$' -benchtime=1x ./internal/media
```

打包工作流在独立 Windows 和 macOS runner 上安装并启动应用。Windows 另外验证卸载。推送到 `verify/**` 分支可运行这一流程。新建标签会在安装验证通过后发布安装包。

## 许可证

SilenceCut 自身代码采用 [MIT 许可证](LICENSE)。安装包的 `Notices` 目录附带 SilenceCut、FFmpeg、Go 及所链接 Go 模块的许可声明。

## 参考行为

AutoCutVideo 2.33.5 的 `/remove/silence` 路由连接 Go 的 `client/remove/silence`，采用 0.1 秒的 FFmpeg 静音检测。原版的三个预设使用波形推算阈值，并对检测片段加前后留白。静态依据来自 REA 的 Rust/Go 检查、内嵌网页资源及 Go 符号与反汇编。SilenceCut 的自定义模式沿用 0.1 秒最低静音时长，三档预设各有最短停顿时长和留白。过滤、波形编辑和导出均独立实现。
