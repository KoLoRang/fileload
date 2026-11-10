#!/usr/bin/env python3
"""从 assets/icon_master.png (1024x1024 RGBA) 生成多尺寸 Windows ICO。

用法:
    python assets/make_ico.py

约定: 小于 256 的帧用传统 32bpp BMP(DIB)+AND 掩码，256 用 PNG 压缩帧，
兼容性最好（VS Code / Chrome 等主流 ico 都这么做）。
"""
import io
import os
import struct

from PIL import Image

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MASTER = os.path.join(ROOT, "assets", "icon_master.png")
OUTS = [
    os.path.join(ROOT, "dist", "fileload.ico"),
    os.path.join(ROOT, "assets", "fileload.ico"),
]
SIZES = [16, 24, 32, 48, 64, 128, 256]


def dib_frame(img: Image.Image) -> bytes:
    """RGBA 图像 -> ICO 用的 BMP 帧（32bpp BI_RGB + AND 掩码）。"""
    s = img.size[0]
    px = img.load()
    rows = []
    for y in range(s - 1, -1, -1):  # DIB 自下而上
        row = bytearray()
        for x in range(s):
            r, g, b, a = px[x, y]
            row += bytes((b, g, r, a))
        rows.append(bytes(row))
    pixels = b"".join(rows)

    mask_stride = ((s + 31) // 32) * 4
    mask = b"\x00" * (mask_stride * s)  # 全 0：保留像素（alpha 已表达透明）

    header = struct.pack(
        "<IiiHHIIiiII",
        40,          # biSize
        s,           # biWidth
        s * 2,       # biHeight = 图像 + 掩码
        1,           # biPlanes
        32,          # biBitCount
        0,           # biCompression = BI_RGB
        len(pixels),
        0, 0, 0, 0,
    )
    return header + pixels + mask


def png_frame(img: Image.Image) -> bytes:
    buf = io.BytesIO()
    img.save(buf, format="PNG", optimize=True)
    return buf.getvalue()


def main() -> None:
    master = Image.open(MASTER).convert("RGBA")
    frames = []
    for s in SIZES:
        small = master.resize((s, s), Image.LANCZOS)
        frames.append((s, png_frame(small) if s >= 256 else dib_frame(small)))

    header = struct.pack("<HHH", 0, 1, len(frames))
    offset = 6 + 16 * len(frames)
    entries, blobs = b"", b""
    for s, data in frames:
        entries += struct.pack(
            "<BBBBHHII", s % 256, s % 256, 0, 0, 1, 32, len(data), offset
        )
        blobs += data
        offset += len(data)

    ico = header + entries + blobs
    for path in OUTS:
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "wb") as fh:
            fh.write(ico)
        print(f"wrote {path} ({len(ico)} bytes, sizes={SIZES})")


if __name__ == "__main__":
    main()
