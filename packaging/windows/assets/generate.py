#!/usr/bin/env python3
"""Generate Windows brand assets for the qabas CLI installer.

Outputs (next to this script):
  qabas.ico    multi-size app icon (16..256 px) for qabas.exe and Add/Remove Programs
  banner.bmp   493x58  WiX UI top banner (WixUIBannerBmp)
  dialog.bmp   493x312 WiX UI welcome/finish background (WixUIDialogBmp)
  License.rtf  license shown in the installer, rendered from ../../../LICENSE

Pass --logo PATH (square PNG, >= 1024 px) to use a real logo instead of the monogram.
Windows Installer bitmap controls only accept BMP/JPEG, so the UI images are BMP.

Requires Pillow: pip install pillow
"""
from __future__ import annotations

import argparse
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]

BRAND_DARK = (11, 37, 69)
BRAND = (20, 83, 160)
BRAND_LIGHT = (56, 139, 224)
WHITE = (255, 255, 255)
ICO_SIZES = [16, 24, 32, 48, 64, 128, 256]


def font(size: int) -> ImageFont.ImageFont:
    for name in ("segoeuib.ttf", "arialbd.ttf", "DejaVuSans-Bold.ttf"):
        for base in (Path("C:/Windows/Fonts"), Path("/usr/share/fonts/truetype/dejavu")):
            p = base / name
            if p.exists():
                return ImageFont.truetype(str(p), size)
    return ImageFont.load_default()


def vertical_gradient(w: int, h: int, top, bottom) -> Image.Image:
    img = Image.new("RGB", (w, h))
    px = img.load()
    for y in range(h):
        t = y / max(h - 1, 1)
        c = tuple(round(top[i] + (bottom[i] - top[i]) * t) for i in range(3))
        for x in range(w):
            px[x, y] = c
    return img


def monogram(size: int = 1024) -> Image.Image:
    grad = vertical_gradient(size, size, BRAND_LIGHT, BRAND_DARK).convert("RGBA")
    mask = Image.new("L", (size, size), 0)
    ImageDraw.Draw(mask).rounded_rectangle((0, 0, size - 1, size - 1), radius=size // 5, fill=255)
    tile = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    tile.paste(grad, (0, 0), mask)

    d = ImageDraw.Draw(tile)
    f = font(int(size * 0.68))
    box = d.textbbox((0, 0), "Q", font=f)
    tw, th = box[2] - box[0], box[3] - box[1]
    d.text(((size - tw) / 2 - box[0], (size - th) / 2 - box[1]), "Q", font=f, fill=WHITE)
    return tile


def load_logo(path: Path | None) -> Image.Image:
    if path is None:
        return monogram()
    img = Image.open(path).convert("RGBA")
    side = max(img.size)
    canvas = Image.new("RGBA", (side, side), (0, 0, 0, 0))
    canvas.paste(img, ((side - img.width) // 2, (side - img.height) // 2))
    return canvas.resize((1024, 1024), Image.LANCZOS)


def write_ico(logo: Image.Image) -> None:
    logo.save(HERE / "qabas.ico", format="ICO", sizes=[(s, s) for s in ICO_SIZES])


def write_banner(logo: Image.Image) -> None:
    w, h = 493, 58
    img = Image.new("RGB", (w, h), WHITE)
    icon = logo.resize((44, 44), Image.LANCZOS)
    img.paste(icon, (w - 44 - 8, 7), icon)
    ImageDraw.Draw(img).line((0, h - 1, w, h - 1), fill=(220, 224, 230))
    img.save(HERE / "banner.bmp", format="BMP")


def write_dialog(logo: Image.Image) -> None:
    w, h, panel = 493, 312, 164
    img = Image.new("RGB", (w, h), WHITE)
    img.paste(vertical_gradient(panel, h, BRAND, BRAND_DARK), (0, 0))
    icon = logo.resize((96, 96), Image.LANCZOS)
    img.paste(icon, ((panel - 96) // 2, 64), icon)

    d = ImageDraw.Draw(img)
    for text, size, y in (("QabasOne", 20, 176), ("CLI", 14, 204)):
        f = font(size)
        box = d.textbbox((0, 0), text, font=f)
        d.text(((panel - (box[2] - box[0])) / 2 - box[0], y), text, font=f, fill=WHITE)
    img.save(HERE / "dialog.bmp", format="BMP")


def rtf_escape(s: str) -> str:
    out = []
    for ch in s:
        if ch in "\\{}":
            out.append("\\" + ch)
        elif ord(ch) > 127:
            out.append(f"\\u{ord(ch)}?")
        else:
            out.append(ch)
    return "".join(out)


def write_license_rtf() -> None:
    text = (ROOT / "LICENSE").read_text(encoding="utf-8").strip()
    paragraphs = [" ".join(p.split()) for p in text.split("\n\n")]
    body = "\\par\\par\n".join(rtf_escape(p) for p in paragraphs)
    rtf = (
        "{\\rtf1\\ansi\\ansicpg1252\\deff0{\\fonttbl{\\f0\\fswiss Segoe UI;}}\n"
        "\\viewkind4\\uc1\\pard\\f0\\fs18\n" + body + "\\par\n}\n"
    )
    (HERE / "License.rtf").write_text(rtf, encoding="ascii", newline="\r\n")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--logo", type=Path, help="square PNG logo (>= 1024 px); defaults to a Q monogram")
    args = ap.parse_args()

    logo = load_logo(args.logo)
    write_ico(logo)
    write_banner(logo)
    write_dialog(logo)
    write_license_rtf()
    print(f"wrote assets to {HERE}")


if __name__ == "__main__":
    main()
