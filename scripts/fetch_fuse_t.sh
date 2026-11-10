#!/bin/sh
# Fetches the pinned FUSE-T installer into build/darwin/vendor and extracts
# what the build needs from it:
#   vendor/fuse-t.pkg            bundled into FileLoad.app (installed on demand)
#   vendor/include/fuse/*.h      libfuse 2 headers for cgofuse (no FUSE-T install needed to build)
#   vendor/FUSE-T-LICENSE.txt    shipped with the app/dmg (required for redistribution)
#
# FUSE-T (https://www.fuse-t.org) is free for non-commercial use; binary
# redistributions must reproduce its copyright notice, hence the license file.
set -eu
cd "$(dirname "$0")/.."

VERSION=1.2.9
SHA256=a1b893740ded8ea034f1acc522ded1144f4a567a1f416daef8c1441cf2662f7f
URL="https://github.com/macos-fuse-t/fuse-t/releases/download/$VERSION/fuse-t-macos-installer-$VERSION.pkg"
VENDOR=build/darwin/vendor
PKG=$VENDOR/fuse-t.pkg

mkdir -p "$VENDOR"
if [ -f "$VENDOR/VERSION" ] && [ "$(cat "$VENDOR/VERSION")" = "$VERSION" ] && [ -f "$PKG" ] &&
	[ -f "$VENDOR/include/fuse/fuse.h" ] && [ -f "$VENDOR/FUSE-T-LICENSE.txt" ]; then
	exit 0 # already prepared
fi

if ! echo "$SHA256  $PKG" | shasum -a 256 -c - >/dev/null 2>&1; then
	echo "downloading FUSE-T $VERSION ..."
	curl -fL --retry 3 -o "$PKG.part" "$URL"
	mv "$PKG.part" "$PKG"
fi
if ! echo "$SHA256  $PKG" | shasum -a 256 -c - >/dev/null 2>&1; then
	echo "FUSE-T installer checksum mismatch: $PKG" >&2
	exit 1
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
pkgutil --expand-full "$PKG" "$TMP/x" >/dev/null
SUPPORT="$TMP/x/fuse-t-core.pkg/Payload/Library/Application Support/fuse-t"
rm -rf "$VENDOR/include"
mkdir -p "$VENDOR/include"
cp -R "$SUPPORT/include/fuse" "$VENDOR/include/fuse"
textutil -convert txt -output "$VENDOR/FUSE-T-LICENSE.txt" "$SUPPORT/LICENSE.rtf"
echo "$VERSION" > "$VENDOR/VERSION"
echo "FUSE-T $VERSION ready in $VENDOR"
