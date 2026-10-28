#!/usr/bin/env python3
"""Checks ios/store/listing.md against App Store Connect's limits and our own rules. Nothing here talks to Apple.

usage: check_listing.py [path/to/listing.md]   (exit 1 with one line per problem)
"""
import re
import sys

# Apple's limits (characters).
LIMITS = {
    "Name": 30,
    "Name fallback": 30,
    "Subtitle": 30,
    "Promotional text": 170,
    "Description": 4000,
    "Keywords": 100,
    "What's New": 4000,
}
REQUIRED = list(LIMITS) + ["Support URL", "Marketing URL", "Privacy Policy URL", "Review notes", "Screenshot captions"]
# Words DA keeps out of captions.
BANNED_IN_CAPTIONS = ["safe", "secure", "ok", "normal", "exact", "measured", "towing"]
EMAIL = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+")


def sections(text):
    out, name, buf = {}, None, []
    for line in text.splitlines():
        m = re.match(r"^## (.+?)\s*$", line)
        if m:
            if name is not None:
                out[name] = "\n".join(buf).strip()
            name, buf = m.group(1), []
        elif name is not None:
            buf.append(line)
    if name is not None:
        out[name] = "\n".join(buf).strip()
    return out


def check(text):
    problems = []
    s = sections(text)
    for name in REQUIRED:
        if not s.get(name):
            problems.append(f"missing or empty section: {name}")
    for name, limit in LIMITS.items():
        if name in s and len(s[name]) > limit:
            problems.append(f"{name} is {len(s[name])} characters, the limit is {limit}")
    kw = s.get("Keywords", "")
    if ", " in kw or kw.endswith(",") or kw.startswith(","):
        problems.append("Keywords: comma-separated with no spaces after the commas")
    for name in ("Support URL", "Marketing URL", "Privacy Policy URL"):
        if name in s and not re.fullmatch(r"https://\S+", s[name]):
            problems.append(f"{name} must be one https URL")
    notes = s.get("Review notes", "")
    if re.search(r"password|passcode|passwd", notes, re.I):
        problems.append("Review notes mention a password: account details go only in App Store Connect's private field")
    if EMAIL.search(notes):
        problems.append("Review notes hold an email address: account details go only in App Store Connect's private field")
    caps = [re.sub(r"^\d+\.\s*", "", l) for l in s.get("Screenshot captions", "").splitlines() if re.match(r"^\d+\.\s", l)]
    if not caps:
        problems.append("Screenshot captions: no numbered captions found")
    for c in caps:
        for w in BANNED_IN_CAPTIONS:
            if re.search(rf"\b{re.escape(w)}\b", c, re.I):
                problems.append(f"caption uses the banned word '{w}': {c}")
    return problems


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else "ios/store/listing.md"
    with open(path, encoding="utf-8") as f:
        problems = check(f.read())
    for p in problems:
        print("FAIL:", p)
    if problems:
        sys.exit(1)
    print("listing ok")


if __name__ == "__main__":
    main()
