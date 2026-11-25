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
    ("charge", "Every charge, with its cost and where it came from"),
    ("truck", "Tires, sleep and health, from your truck"),
    ("trust", "Sends no commands to your truck. Your Rivian password is never stored."),
]
FOOTER = "Sample truck, generated data"
SKIP_FOOTER = {"trust"}  # the sign-in screen is not the demo truck
# The sign-in screen is a card on an empty page: only the card and a margin are shown, without the status bar, so it reads as a
# panel rather than a phone with its bottom cut off. Where the card is differs by screen height, so it is found in the picture
# (card_rows), not given as fractions of it. The caption and the card are centred together.
CROP = {"trust"}
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


def card_box(shot):
    """The sign-in card's box (left, top, right, bottom): from a point inside it, the run of pixels of about the card's own fill,
    up, down, left and right. The page's glow, the status bar and the card's shadow differ from the fill by more and are left out."""
    W, H = shot.size
    ref_x, ref_y = int(W * 0.06), int(H * 0.35)
    ref = shot.getpixel((ref_x, ref_y))
    # The dark card has a visible gradient (it needs room) and sits close to its page; the light card is nearly the page's colour.
    tol = 10 if max(ref) < 64 else 3

    def fill(x, y):
        return max(abs(p - q) for p, q in zip(shot.getpixel((x, y)), ref)) <= tol

    top, bottom, left, right = ref_y, ref_y, ref_x, ref_x
    while top > 0 and fill(ref_x, top - 1):
        top -= 1
    while bottom < H - 1 and fill(ref_x, bottom + 1):
        bottom += 1
    left, right = ref_x, ref_x
    for y in range(top + 1, bottom, max(1, int(H * 0.01))):  # rows with text in them stop the run early; blank rows give the card's width
        if not fill(ref_x, y):
            continue
        l, r = ref_x, ref_x
        while l > 0 and fill(l - 1, y):
            l -= 1
        while r < W - 1 and fill(r + 1, y):
            r += 1
        left, right = min(left, l), max(right, r)
    if bottom - top < H * 0.2 or right - left < W * 0.6:
        raise SystemExit("no sign-in card found in the picture")
    return left, top, right + 1, bottom + 1


def frame(raw_path, caption, footer, mode):
    pal = PALETTE[mode]
    shot = Image.open(raw_path).convert("RGB")
    W, H = shot.size
    crop = os.path.basename(raw_path).split("-")[0] in CROP
    if crop:
        shot = shot.crop(card_box(shot))
    canvas = Image.new("RGB", (W, H), pal["canvas"])
    d = ImageDraw.Draw(canvas)
    # Caption: up to two lines, centred, in the app's type.
    cap_font = font("Geist-SemiBold.ttf", W * 0.062)
    lines = wrap(d, caption, cap_font, W * 0.86)
    line_h = W * 0.078
    y = H * 0.045
    # The screen, scaled to fit between the caption and the footer, with rounded corners and a hairline.
    top = max(y + len(lines) * line_h + H * 0.02, H * 0.15)
    bottom = H * (0.945 if footer else 0.975)
    shot_w, shot_h = shot.size
    scale = min((bottom - top) / shot_h, 0.92 if crop else 0.86)
    sw, sh = int(shot_w * scale), int(shot_h * scale)
    scr = shot.resize((sw, sh), Image.LANCZOS)
    radius = int(sw * (0.03 if crop else 0.07))
    mask = Image.new("L", (sw, sh), 0)
    ImageDraw.Draw(mask).rounded_rectangle((0, 0, sw - 1, sh - 1), radius=radius, fill=255)
    if crop:  # a short (cropped) screen: the caption and the card are one block, centred on the picture
        gap = H * 0.04
        block = len(lines) * line_h + gap + sh
        y = max(H * 0.045, (H - block) / 2)
        y0 = int(y + len(lines) * line_h + gap)
    else:
        y0 = int(top + max(0, (bottom - top - sh) / 2))
    x0 = (W - sw) // 2
    for ln in lines:
        w = d.textlength(ln, font=cap_font)
        d.text(((W - w) / 2, y), ln, font=cap_font, fill=pal["text"])
        y += line_h
    canvas.paste(scr, (x0, y0), mask)
    if not crop:  # a cropped card has its own border; an outline around it would read as a phone frame
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
