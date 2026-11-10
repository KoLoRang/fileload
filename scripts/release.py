#!/usr/bin/env python3
"""FileLoad GitHub Releases helper (stdlib + gh CLI).

Creates release v<ver> on github.com/KoLoRang/fileload and uploads the
installers. The app's About page reads the latest release and auto-updates:
  Windows: asset name contains "Setup" and ends with .exe (Inno Setup)
  macOS:   asset name ends with .dmg (contains FileLoad.app)

    gh auth login                                   # once
    python scripts/release.py publish 1.0.1 dist/FileLoad-Setup-1.0.1.exe dist/FileLoad-1.0.1-macos.dmg [--notes notes.md]

The tag is created from the current HEAD if it does not exist yet; push your
commits first.
"""
import argparse
import os
import subprocess
import sys

REPO = "KoLoRang/fileload"


def gh(*args):
    r = subprocess.run(["gh", *args], text=True)
    if r.returncode:
        sys.exit(f"gh {' '.join(args[:2])} 失败（退出码 {r.returncode}）")


def cmd_publish(args):
    ver = args.version.lstrip("v")
    for f in args.assets:
        if not os.path.isfile(f):
            sys.exit(f"找不到文件：{f}")
        n = os.path.basename(f).lower()
        if not ((n.endswith(".exe") and "setup" in n) or n.endswith(".dmg")):
            sys.exit(f"{f}：安装包需为 *Setup*.exe 或 *.dmg，否则程序内自动更新找不到它")
    notes = ["--notes-file", args.notes] if args.notes else ["--generate-notes"]
    gh("release", "create", f"v{ver}", "--repo", REPO, "--title", f"v{ver}", *notes, *args.assets)
    print(f"已发布 v{ver}: https://github.com/{REPO}/releases/tag/v{ver}")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("publish", help="创建 release 并上传安装包")
    p.add_argument("version")
    p.add_argument("assets", nargs="+", help="安装包：FileLoad-Setup-<ver>.exe 和/或 FileLoad-<ver>-macos.dmg")
    p.add_argument("--notes", help="发布说明 Markdown 文件（缺省自动生成）")
    p.set_defaults(fn=cmd_publish)
    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
