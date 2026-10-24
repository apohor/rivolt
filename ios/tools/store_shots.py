#!/usr/bin/env python3
"""App Store screenshots: a raw simulator screenshot of a demo screen, framed with a caption above and an honest
"Sample truck, generated data" line below, at the device's exact pixel size.

usage: store_shots.py <raw dir> <out dir> <dark|light> [only-name ...]
The raw dir holds <name>.png for each shot in CAPTIONS (the demo truck's screens, shot in a simulator)."""
import os
import sys

from PIL import Image, ImageDraw, ImageFont

FONTS = os.path.join(os.path.dirname(__file__), "..", "Rivolt", "Resources", "Fonts")
# One short line each (growth's copy); none claims a comparison or a safety.
CAPTIONS = [
    ("now", "Your Rivian at a glance"),
    ("money", "What charging actually cost"),
    ("drive", "Every drive, route and charts"),
    ("charge", "Every charge, priced at your rate"),
    ("truck", "Tires, sleep and health, from your truck"),
    ("trust", "Sends no commands to your truck. Your Rivian password is never stored."),
]
FOOTER = "Sample truck, generated data"
SKIP_FOOTER = {"trust"}  # the sign-in screen is not the demo truck
PALETTE = {
    "dark": {"canvas": (11, 15, 21), "text": (245, 245, 245), "muted": (146, 146, 146), "edge": (48, 54, 64)},
    "light": {"canvas": (233, 236, 241), "text": (23, 23, 23), "muted": (99, 99, 99), "edge": (200, 205, 214)},
}


def font(name, px):
    return ImageFont.truetype(os.path.join(FONTS, name), int(px))


def wrap(draw, text, fnt, width):
    words, lines, cur = text.split(), [], ""
    for w in words:
        t = (cur + " " + w).strip()
        if draw.textlength(t, font=fnt) <= width or not cur:
            cur = t
        else:
            lines.append(cur)
            cur = w
    lines.append(cur)
    return lines


def frame(raw_path, caption, footer, mode):
    pal = PALETTE[mode]
    shot = Image.open(raw_path).convert("RGB")
    W, H = shot.size
    canvas = Image.new("RGB", (W, H), pal["canvas"])
    d = ImageDraw.Draw(canvas)
    # Caption: up to two lines, centred, in the app's type.
    cap_font = font("Geist-SemiBold.ttf", W * 0.062)
    lines = wrap(d, caption, cap_font, W * 0.86)
    y = H * 0.045
    for ln in lines:
        w = d.textlength(ln, font=cap_font)
        d.text(((W - w) / 2, y), ln, font=cap_font, fill=pal["text"])
        y += W * 0.078
    # The screen, scaled to fit between the caption and the footer, with rounded corners and a hairline.
    top = max(y + H * 0.02, H * 0.15)
    bottom = H * (0.945 if footer else 0.975)
    scale = min((bottom - top) / H, 0.86)
    sw, sh = int(W * scale), int(H * scale)
    scr = shot.resize((sw, sh), Image.LANCZOS)
    radius = int(sw * 0.07)
    mask = Image.new("L", (sw, sh), 0)
    ImageDraw.Draw(mask).rounded_rectangle((0, 0, sw - 1, sh - 1), radius=radius, fill=255)
    x0, y0 = (W - sw) // 2, int(top)
    canvas.paste(scr, (x0, y0), mask)
    d.rounded_rectangle((x0, y0, x0 + sw - 1, y0 + sh - 1), radius=radius, outline=pal["edge"], width=max(2, W // 400))
    if footer:
        ff = font("Geist-Regular.ttf", W * 0.026)
        w = d.textlength(footer, font=ff)
        d.text(((W - w) / 2, H * 0.958), footer, font=ff, fill=pal["muted"])
    return canvas


def main():
    raw, out, mode = sys.argv[1], sys.argv[2], sys.argv[3]
    only = set(sys.argv[4:])
    os.makedirs(out, exist_ok=True)
    made = 0
    for name, caption in CAPTIONS:
        if only and name not in only:
            continue
        src = os.path.join(raw, f"{name}-{mode}.png")
        if not os.path.exists(src):
            print(f"missing {src}", file=sys.stderr)
            continue
        img = frame(src, caption, None if name in SKIP_FOOTER else FOOTER, mode)
        img.save(os.path.join(out, f"{name}-{mode}.png"), optimize=True)
        made += 1
        print(f"{name}-{mode}: {img.size[0]}x{img.size[1]}")
    sys.exit(0 if made else 1)


if __name__ == "__main__":
    main()
