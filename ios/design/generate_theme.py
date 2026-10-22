#!/usr/bin/env python3
"""Generates ios/Rivolt/Theme/ThemeTokens.swift from ios/design/tokens.json.

tokens.json is generated from the web CSS (see ios/design/README.md); this turns it into Swift constants so no colour
or size is typed twice. Run from the repo root after a deliberate web change:

    python3 ios/design/generate_theme.py

ThemeTokensTests fails when the committed Swift and tokens.json disagree.
"""
import json
import pathlib

here = pathlib.Path(__file__).resolve().parent
tokens = json.loads((here / "tokens.json").read_text())
out = here.parent / "Rivolt" / "Theme" / "ThemeTokens.swift"


def ident(name: str) -> str:
    return name[0].lower() + name[1:]


def rgba(c: dict) -> str:
    h = c["hex"].lstrip("#").upper()
    alpha = c["alpha"]
    a = "1" if alpha == 1 else repr(float(alpha))
    return f"Rgba(0x{h}, {a})"


def pair(v: dict) -> str:
    if "both" in v:
        c = rgba(v["both"])
        return f"Pair(dark: {c}, light: {c})"
    return f"Pair(dark: {rgba(v['dark'])}, light: {rgba(v['light'])})"


lines = [
    "// Generated from ios/design/tokens.json by ios/design/generate_theme.py. Do not edit by hand:",
    "// change the web, regenerate tokens.json, then run the script. ThemeTokensTests checks the two agree.",
    "import Foundation",
    "",
    "enum ThemeTokens {",
    "    /// sRGB as hex with an alpha, as tokens.json carries it.",
    "    struct Rgba: Equatable {",
    "        let hex: UInt32",
    "        let alpha: Double",
    "        init(_ hex: UInt32, _ alpha: Double = 1) { self.hex = hex; self.alpha = alpha }",
    "    }",
    "",
    "    /// A colour in dark and in light.",
    "    struct Pair: Equatable {",
    "        let dark: Rgba",
    "        let light: Rgba",
    "    }",
    "",
    "    // MARK: Roles (semantic colours)",
]
for n, v in tokens["roles"].items():
    lines.append(f"    static let {ident(n)} = {pair(v)}")
lines += ["", "    // MARK: Literals (glows, gauge gradients, shadows, chart colours)"]
for n, v in tokens["literals"].items():
    lines.append(f"    static let {ident(n)} = {pair(v)}")

lines += ["", "    // MARK: Scalars (points; milliseconds for motion; em for tracking)"]
for n, v in tokens["scalars"].items():
    if isinstance(v["value"], str):
        continue
    head, _, tail = n.partition(".")
    name = head + tail[0].upper() + tail[1:]
    lines.append(f"    static let {name}: Double = {v['value']}")
lines.append("}")
out.write_text("\n".join(lines) + "\n")
print(f"wrote {out}")
