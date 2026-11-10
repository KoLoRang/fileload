#!/bin/sh
# Builds dist/FileLoad.app and dist/FileLoad-<version>-macos.dmg.
#   scripts/build_macos.sh                  # native arch
#   ARCH=universal scripts/build_macos.sh   # arm64 + amd64
#   NODMG=1 scripts/build_macos.sh          # skip the dmg (faster dev builds)
#
# FUSE-T does NOT need to be installed on the build machine: the pinned
# installer is fetched into build/darwin/vendor (scripts/fetch_fuse_t.sh),
# its headers are used for cgo, and the installer itself is bundled into the
# app so users can install the driver from inside FileLoad.
set -eu
cd "$(dirname "$0")/.."

scripts/fetch_fuse_t.sh
VENDOR=$PWD/build/darwin/vendor
VERSION=$(sed -n 's/^var Version = "\(.*\)"/\1/p' internal/update/update.go)
APP=dist/FileLoad.app
DMG=dist/FileLoad-$VERSION-macos.dmg

export CGO_ENABLED=1
export CGO_CFLAGS="-mmacosx-version-min=13.0 -I$VENDOR/include/fuse"
export CGO_LDFLAGS="-mmacosx-version-min=13.0"
export MACOSX_DEPLOYMENT_TARGET=13.0

# optional: bake a read-only GitHub token (fine-grained, this repo, Contents:
# read) into the app so users of the private repo get updates without one
LDFLAGS="-s -w"
[ -n "${FILELOAD_GH_TOKEN:-}" ] && LDFLAGS="$LDFLAGS -X fileload/internal/update.EmbeddedToken=$FILELOAD_GH_TOKEN"

build() { # $1 = GOARCH, $2 = output
	GOARCH=$1 go build -trimpath -tags production -ldflags "$LDFLAGS" -o "$2" ./cmd/fileload
}

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources/ThirdParty"
if [ "${ARCH:-}" = universal ]; then
	build arm64 dist/fileload-arm64
	build amd64 dist/fileload-amd64
	lipo -create -output "$APP/Contents/MacOS/FileLoad" dist/fileload-arm64 dist/fileload-amd64
	rm -f dist/fileload-arm64 dist/fileload-amd64
else
	build "$(go env GOARCH)" "$APP/Contents/MacOS/FileLoad"
fi
sed "s/@VERSION@/$VERSION/g" build/darwin/Info.plist > "$APP/Contents/Info.plist"

# bundled driver installer + its license (redistribution condition)
cp "$VENDOR/fuse-t.pkg" "$APP/Contents/Resources/fuse-t.pkg"
cp "$VENDOR/FUSE-T-LICENSE.txt" "$APP/Contents/Resources/ThirdParty/FUSE-T-LICENSE.txt"

# icon.icns from the 1024px master
ICONSET=$(mktemp -d)/icon.iconset
mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
	sips -z $s $s assets/icon_master.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
	sips -z $((s * 2)) $((s * 2)) assets/icon_master.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/icon.icns"

# ad-hoc signature (a Developer ID + notarization is needed for public
# distribution without the right-click > Open step)
codesign --force --deep --sign - "$APP" >/dev/null 2>&1 || true
echo "built $APP ($VERSION)"

[ -n "${NODMG:-}" ] && exit 0

# dmg: the app, a shortcut to /Applications and the third-party notices
STAGE=$(mktemp -d)/FileLoad
mkdir -p "$STAGE"
cp -R "$APP" "$STAGE/"
ln -s /Applications "$STAGE/Applications"
cat > "$STAGE/第三方组件许可.txt" <<EOF
FileLoad $VERSION 内置以下第三方组件：

FUSE-T $(cat "$VENDOR/VERSION")（https://www.fuse-t.org）
用于在 macOS 上把远程目录挂载到本地，无需内核扩展。首次挂载时 FileLoad 会提示
一键安装（需要输入电脑密码）。其许可条款如下：

$(cat "$VENDOR/FUSE-T-LICENSE.txt")
EOF
rm -f "$DMG"
hdiutil create -quiet -volname "FileLoad $VERSION" -srcfolder "$STAGE" -fs HFS+ -format UDZO -ov "$DMG"
echo "built $DMG ($(du -h "$DMG" | cut -f1))"
