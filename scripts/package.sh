#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
root=$(pwd)
work=${SILENCECUT_FFMPEG_WORK:-"$root/.ffmpeg"}
dest="$root/dist"
version_input=${SILENCECUT_VERSION:-}
if [ -z "$version_input" ]; then
  version_input=$(git describe --tags --always --dirty 2>/dev/null || true)
fi
version=$(printf '%s\n' "$version_input" | sed -nE 's/^v?([0-9]+\.[0-9]+\.[0-9]+)([-+].*)?$/\1/p')
if [ -z "$version" ]; then
  echo "Cannot derive a semantic version from: ${version_input:-no git tag or SILENCECUT_VERSION}" >&2
  exit 1
fi

export CGO_ENABLED=1
mkdir -p "$dest"

copy_notice() {
  if [ -f "$2" ]; then chmod u+w "$2"; fi
  cat "$1" > "$2"
}

copy_module_notices() {
  notices=$1
  binary=$2
  modules=$(go version -m "$binary" | awk '$1 == "dep" {print $2}')
  mkdir -p "$notices/Go"
  goroot=$(go env GOROOT)
  if [ "$GOOS" = windows ]; then goroot=$(cygpath -u "$goroot"); fi
  copy_notice "$goroot/LICENSE" "$notices/Go/LICENSE"
  copy_notice "$goroot/PATENTS" "$notices/Go/PATENTS"
  for module in $modules; do
    module_dir=$(go list -m -f '{{.Dir}}' "$module")
    if [ "$GOOS" = windows ]; then module_dir=$(cygpath -u "$module_dir"); fi
    target="$notices/Go/$module"
    mkdir -p "$target"
    copy_notice "$module_dir/LICENSE" "$target/LICENSE"
    for notice in NOTICE PATENTS; do
      if [ -f "$module_dir/$notice" ]; then
        copy_notice "$module_dir/$notice" "$target/$notice"
      fi
    done
  done
}

case "$(uname -s)" in
  MINGW*|MSYS*|UCRT*)
    compiler=${ISCC:-$(command -v ISCC.exe || true)}
    if [ -z "$compiler" ]; then
      compiler="$(cygpath -u "$(printenv 'ProgramFiles(x86)')")/Inno Setup 6/ISCC.exe"
    fi
    if [ ! -x "$compiler" ]; then
      echo "Inno Setup 6 compiler not found. Set ISCC to the path to ISCC.exe." >&2
      exit 1
    fi
    sdk="$work/mingw32-x86_64"
    bundle="$dest/SilenceCut-windows-x64"
    mkdir -p "$bundle/Notices/FFmpeg"
    resource="$root/appicon_windows_amd64.syso"
    if [ -e "$resource" ]; then echo "Icon resource already exists: $resource" >&2; exit 1; fi
    icon_work=$(mktemp -d "$dest/.app-icon.XXXXXX")
    trap 'rm -f "$resource" "$icon_work/SilenceCut.ico" "$icon_work/icon.rc"; rmdir "$icon_work"' EXIT
    go run ./scripts/make-icon.go assets/silencecut.svg "$icon_work/SilenceCut.ico"
    printf '1 ICON "%s"\n' "$(cygpath -m "$icon_work/SilenceCut.ico")" > "$icon_work/icon.rc"
    windres -F pe-x86-64 -i "$icon_work/icon.rc" -o "$resource"
    export GOOS=windows GOARCH=amd64
    sdk_native=$(cygpath -m "$sdk")
    export CGO_CFLAGS="'-I$sdk_native/include'"
    export CGO_LDFLAGS="'-L$sdk_native/lib'"
    go build -trimpath -tags mygo_nowebview -ldflags=-H=windowsgui -o "$bundle/SilenceCut.exe" .
    cp "$sdk"/bin/*.dll "$bundle/"
    imports=$(objdump -p "$bundle"/*.dll "$bundle/SilenceCut.exe")
    if printf '%s\n' "$imports" | grep -qi 'DLL Name: libwinpthread-1\.dll[[:space:]]*$'; then
      runtime=$(cygpath -u "$(gcc -print-file-name=libwinpthread-1.dll)")
      # MSYS2 may only expose the runtime DLL next to the GCC executable.
      if [ ! -f "$runtime" ]; then
        runtime="$(dirname "$(command -v gcc)")/libwinpthread-1.dll"
      fi
      if [ ! -f "$runtime" ]; then
        echo "Required MinGW runtime DLL not found: libwinpthread-1.dll" >&2
        exit 1
      fi
      cp "$runtime" "$bundle/"
    fi
    cp "$sdk/notice/"* "$bundle/Notices/FFmpeg/"
    copy_notice LICENSE "$bundle/Notices/SilenceCut.txt"
    cp README.md "$bundle/使用说明.md"
    copy_module_notices "$bundle/Notices" "$bundle/SilenceCut.exe"
    MSYS2_ARG_CONV_EXCL='*' "$compiler" "/DAppVersion=$version" \
      "/DBundleDir=$(cygpath -w "$bundle")" "/DOutputDir=$(cygpath -w "$dest")" \
      "/DIconFile=$(cygpath -w "$icon_work/SilenceCut.ico")" "$(cygpath -w "$root/scripts/installer.iss")"
    ;;
  Darwin)
    app="$dest/SilenceCut.app"
    contents="$app/Contents"
    mkdir -p "$contents/MacOS" "$contents/Frameworks" "$contents/Resources/Notices/FFmpeg"
    go run ./scripts/make-icon.go assets/silencecut.svg "$contents/Resources/AppIcon.icns"
    sdk="$work/darwin-arm64"
    export GOOS=darwin GOARCH=arm64 MACOSX_DEPLOYMENT_TARGET=14.0 CC="clang -arch arm64"
    export CGO_CFLAGS="'-I$sdk/include'"
    export CGO_LDFLAGS="'-L$sdk/lib' -Wl,-rpath,@executable_path/../Frameworks"
    go build -trimpath -o "$contents/MacOS/SilenceCut" .
    chmod +x "$contents/MacOS/SilenceCut"
    for lib in "$sdk/lib/"lib*.dylib; do
      name=$(basename "$lib")
      case "$name" in
        lib*.*.*.dylib|lib*.[!0-9]*.dylib) continue ;;
        lib*.[0-9]*.dylib) ;;
        *) continue ;;
      esac
      cp -L "$lib" "$contents/Frameworks/$name"
    done
    cp "$sdk"/notice/* "$contents/Resources/Notices/FFmpeg/"
    copy_notice LICENSE "$contents/Resources/Notices/SilenceCut.txt"
    cp README.md "$contents/Resources/使用说明.md"
    copy_module_notices "$contents/Resources/Notices" "$contents/MacOS/SilenceCut"
    cat > "$contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleExecutable</key><string>SilenceCut</string>
  <key>CFBundleIdentifier</key><string>app.silencecut.desktop</string>
  <key>CFBundleName</key><string>极速过滤</string>
  <key>CFBundleIconFile</key><string>AppIcon.icns</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$version</string>
  <key>CFBundleVersion</key><string>$version</string>
  <key>CFBundleDevelopmentRegion</key><string>zh-Hans</string>
  <key>LSMinimumSystemVersion</key><string>14.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
    for lib in "$contents/Frameworks/"*.dylib; do
      codesign --force --sign - "$lib"
    done
    codesign --force --sign - "$app"
    codesign --verify --strict --deep "$app"
    package="$dest/SilenceCut-macos-arm64.pkg"
    productbuild --component "$app" /Applications "$package"
    pkgutil --payload-files "$package" | grep -E '(^|/)SilenceCut\.app/Contents/MacOS/SilenceCut$'
    ;;
  *) echo "Unsupported build host" >&2; exit 1 ;;
esac
