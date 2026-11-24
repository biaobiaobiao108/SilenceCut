#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")/.."
root=$(pwd)
work=${SILENCECUT_FFMPEG_WORK:-"$root/.ffmpeg"}
dest="$root/dist"

export CGO_ENABLED=1
mkdir -p "$dest"

copy_notice() {
  if [ -f "$2" ]; then chmod u+w "$2"; fi
  cat "$1" > "$2"
}

case "$(uname -s)" in
  MINGW*|MSYS*)
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
    # MinGW's shared runtime is needed only when the toolchain selected it.
    if objdump -p "$bundle"/*.dll "$bundle/SilenceCut.exe" | grep -qi 'DLL Name: libwinpthread-1.dll'; then
      runtime=$(gcc -print-file-name=libwinpthread-1.dll)
      cp "$(cygpath -u "$runtime")" "$bundle/"
    fi
    cp "$sdk/notice/"* "$bundle/Notices/FFmpeg/"
    cp README.md "$bundle/使用说明.md"
    mygo_dir=$(go list -m -f '{{.Dir}}' github.com/egoist/mygo)
    copy_notice "$(cygpath -u "$mygo_dir")/LICENSE" "$bundle/Notices/Mygo.txt"
    "$(cygpath -u "$SYSTEMROOT")/System32/tar.exe" -a -cf "$(cygpath -m "$dest/SilenceCut-windows-x64.zip")" \
      -C "$(cygpath -m "$dest")" "$(basename "$bundle")"
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
    cp README.md "$contents/Resources/使用说明.md"
    copy_notice "$(go list -m -f '{{.Dir}}' github.com/egoist/mygo)/LICENSE" "$contents/Resources/Notices/Mygo.txt"
    cat > "$contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleExecutable</key><string>SilenceCut</string>
  <key>CFBundleIdentifier</key><string>app.silencecut.desktop</string>
  <key>CFBundleName</key><string>极速过滤</string>
  <key>CFBundleIconFile</key><string>AppIcon.icns</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0.0</string>
  <key>CFBundleVersion</key><string>1</string>
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
    ditto -c -k --keepParent "$app" "$dest/SilenceCut-macos-arm64.zip"
    ;;
  *) echo "Unsupported build host" >&2; exit 1 ;;
esac
