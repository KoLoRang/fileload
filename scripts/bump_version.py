#!/usr/bin/env python3
"""Sets the app version in every file that carries it.

    python scripts/bump_version.py 1.0.1
    git commit -am "v1.0.1" && git tag v1.0.1 && git push --follow-tags

Pushing the tag makes .github/workflows/release.yml build the macOS and
Windows installers and publish them as a GitHub Release; that workflow
refuses to run when the tag and internal/update/update.go disagree.
"""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent


def main():
    if len(sys.argv) != 2 or not re.fullmatch(r"\d+\.\d+\.\d+", sys.argv[1].lstrip("v")):
        sys.exit("usage: bump_version.py <major.minor.patch>")
    ver = sys.argv[1].lstrip("v")
    nums = ",".join(ver.split(".") + ["0"])
    edits = {
        "internal/update/update.go": [(r'^var Version = ".*"$', f'var Version = "{ver}"')],
        "build/windows/app.rc": [
            (r"^( (?:FILE|PRODUCT)VERSION) [\d,]+$", rf"\g<1> {nums}"),
            (r'^(\s*VALUE "(?:File|Product)Version", )".*"$', rf'\g<1>"{ver}"'),
        ],
        "build/windows/installer.iss": [(r'^#define MyAppVersion ".*"$', f'#define MyAppVersion "{ver}"')],
    }
    for rel, subs in edits.items():
        path = ROOT / rel
        text = path.read_text(encoding="utf-8")
        for pattern, repl in subs:
            text, n = re.subn(pattern, repl, text, flags=re.M)
            if n == 0:
                sys.exit(f"{rel}: pattern not found: {pattern}")
        path.write_text(text, encoding="utf-8")
        print(f"updated {rel}")
    print(f"version is now {ver}")


if __name__ == "__main__":
    main()
