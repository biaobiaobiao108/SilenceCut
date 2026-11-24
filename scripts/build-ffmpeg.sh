#!/usr/bin/env sh
set -eu

version=8.1.3
sha=7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
work=${SILENCECUT_FFMPEG_WORK:-"$root/.ffmpeg"}
host=$(uname -s)
case "$host" in
  MINGW*|MSYS*|UCRT*)
    arch=x86_64
    target=mingw32
    command -v nasm >/dev/null || { echo "Install NASM and add it to PATH to build the Windows SDK." >&2; exit 1; }
    platform_flags='--enable-mediafoundation --enable-d3d11va --disable-d3d12va --disable-dxva2'
    encoder=h264_mf
    # FFmpeg 8.1 builds the shared D3D11 decoder objects with the non-suffixed switches.
    hwaccels=h264_d3d11va,h264_d3d11va2,hevc_d3d11va,hevc_d3d11va2,av1_d3d11va,av1_d3d11va2,vp9_d3d11va,vp9_d3d11va2,mpeg2_d3d11va,mpeg2_d3d11va2,vc1_d3d11va,vc1_d3d11va2
    hw_scale=scale_d3d11
    ;;
  Darwin)
    arch=arm64
    target=darwin
    platform_flags='--enable-videotoolbox --install-name-dir=@rpath'
    if [ "$arch" != "$(uname -m)" ]; then
      platform_flags="$platform_flags --enable-cross-compile"
    fi
    encoder=h264_videotoolbox
    hwaccels=h264_videotoolbox,hevc_videotoolbox,av1_videotoolbox,vp9_videotoolbox,mpeg1_videotoolbox,mpeg2_videotoolbox,mpeg4_videotoolbox,prores_videotoolbox
    hw_scale=scale_vt
    export MACOSX_DEPLOYMENT_TARGET=14.0
    export CC="clang -arch $arch"
    ;;
  *) echo "Unsupported build host: $host" >&2; exit 1 ;;
esac

mkdir -p "$work"
work=$(CDPATH= cd -- "$work" && pwd)
sdk="$work/$target-$arch"
archive="$work/ffmpeg-$version.tar.xz"
if [ ! -f "$archive" ]; then
  curl --retry 2 -fsSL "https://ffmpeg.org/releases/ffmpeg-$version.tar.xz" -o "$archive.part"
  mv "$archive.part" "$archive"
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$archive" | cut -d ' ' -f 1)
else
  actual=$(shasum -a 256 "$archive" | cut -d ' ' -f 1)
fi
[ "$actual" = "$sha" ] || { echo "FFmpeg source digest mismatch: $archive" >&2; exit 1; }

src="$work/source-$target-$arch"
mkdir -p "$src"
if [ ! -f "$src/configure" ]; then
  tar -xf "$archive" --strip-components 1 -C "$src"
fi
cd "$src"
if [ "$target" = mingw32 ]; then
  # Define COM interface IDs for the standalone filter DLL.
  # Use separate render targets for the scaler. Some drivers reject a
  # multi-plane texture array with these bind flags.
  # Pass the expanded x86 object list through a response file on Windows.
  for patch_file in "$root/scripts/ffmpeg-d3d11-scale.patch" "$root/scripts/ffmpeg-mingw-response-files.patch"; do
    if ! patch --dry-run --force -R -p1 < "$patch_file" >/dev/null 2>&1; then
      patch --force -p1 < "$patch_file"
    fi
  done
fi
# Keep the runtime focused on local media, with the platform H.264 encoder and native AAC/PCM.
./configure --prefix="$sdk" --arch="$arch" --target-os="$target" \
  --disable-programs --disable-doc --disable-static --enable-shared \
  --disable-autodetect --disable-network --disable-avdevice \
  --disable-everything --disable-hwaccels --enable-hwaccel="$hwaccels" --enable-protocol=file \
  --enable-demuxer=mov,matroska,avi,mpegps,mpegts,flv,asf,mp3,wav,flac,ogg,aac,aiff,caf,ac3 \
  --enable-decoder='h264,hevc,av1,vp8,vp9,mpeg4,mpeg1video,mpeg2video,mjpeg,prores,dnxhd,ffv1,flv,wmv1,wmv2,wmv3,vc1,theora,rawvideo,png,aac,aac_latm,mp3,flac,opus,vorbis,alac,ac3,eac3,wmav1,wmav2,wmapro,wavpack,adpcm_*,pcm_*' \
  --enable-parser=h264,hevc,av1,vp8,vp9,mpeg4video,mpegvideo,mjpeg,aac,aac_latm,mpegaudio,flac,opus,vorbis,ac3,vc1 \
  --enable-filter="aresample,aformat,$hw_scale" \
  --enable-muxer=mp4,wav --enable-encoder="$encoder,aac,pcm_s24le" \
  --enable-bsf=aac_adtstoasc,av1_frame_merge,extract_extradata $platform_flags
make -j "${SILENCECUT_JOBS:-8}"
make install
mkdir -p "$sdk/notice"
cp "$archive" COPYING.LGPLv2.1 COPYING.LGPLv3 LICENSE.md ffbuild/config.mak "$sdk/notice/"
if [ "$target" = mingw32 ]; then
  cp "$root/scripts/ffmpeg-d3d11-scale.patch" "$root/scripts/ffmpeg-mingw-response-files.patch" "$sdk/notice/"
fi
