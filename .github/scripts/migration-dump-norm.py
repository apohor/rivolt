#!/usr/bin/env python3
"""migration-dump-norm.py A B: compare two pg_dump files as sets of statements; exit 1 and print the difference when they differ.

Why sets of normalised statements and not diff: pg_dump orders objects by dependency, and that order moves when a table is created by a
different route (CREATE then ALTER versus one CREATE). What must be equal is the schema, the seed rows and the grants, not the order they
are written in. A CREATE TABLE is split into one item per column and constraint, so a column order or a default shows as a single line.

Skipped: comments (outside quotes only), psql meta lines, the clock in seed rows (read as <ts>), SET/set_config/SELECT preamble lines, and the vehicle_state_yYYYY_MM partitions, which the migration that creates
the partitioned table makes relative to the day it runs (a dump on another day names other months).
"""
import re
import sys


DOLLAR = re.compile(r"\$([A-Za-z_][A-Za-z0-9_]*)?\$")


def quoted_end(text, i):
    """Index just past the quoted run that starts at text[i] (a ' or "). A doubled quote is a literal quote; inside an E'...' string a
    backslash also escapes the next character."""
    c, n = text[i], len(text)
    escapes = c == "'" and i > 0 and text[i - 1] in "eE" and (i < 2 or not (text[i - 2].isalnum() or text[i - 2] == "_"))
    j = i + 1
    while j < n:
        if escapes and text[j] == "\\":
            j += 2
            continue
        if text[j] == c:
            if text.startswith(c * 2, j):
                j += 2
                continue
            return j + 1
        j += 1
    return n


def dollar_end(text, i):
    """Index just past the $tag$...$tag$ body that starts at text[i], or None when text[i] does not open one."""
    m = DOLLAR.match(text, i)
    if not m:
        return None
    tag = m.group(0)
    j = text.find(tag, m.end())
    return len(text) if j < 0 else j + len(tag)


def statements(text):
    """Split a dump into statements, reading quotes the way psql does: a `--` inside a string is text, a `;` inside a string or a $$ body does
    not end the statement, and quoted text is kept byte for byte (whitespace is collapsed outside quotes only)."""
    out, cur = [], []
    i, n = 0, len(text)
    line_start = True
    while i < n:
        c = text[i]
        if line_start and c == "\\":  # a psql meta line (\restrict TOKEN): random per dump
            while i < n and text[i] != "\n":
                i += 1
            continue
        line_start = c == "\n"
        if c == "-" and text.startswith("--", i):
            while i < n and text[i] != "\n":
                i += 1
            continue
        if c == "/" and text.startswith("/*", i):
            j = text.find("*/", i + 2)
            i = n if j < 0 else j + 2
            cur.append(" ")
            continue
        if c == "'" or c == '"':
            j = quoted_end(text, i)
            cur.append(text[i:j])
            i = j
            continue
        if c == "$":
            j = dollar_end(text, i)
            if j is not None:
                cur.append(text[i:j])
                i = j
                continue
        if c == ";":
            out.append("".join(cur))
            cur = []
            i += 1
            continue
        cur.append(c)
        i += 1
    if "".join(cur).strip():
        out.append("".join(cur))
    return out


def squeeze(st):
    """Collapse whitespace outside quotes and dollar quotes (tagged or bare)."""
    res, i, n, start = [], 0, len(st), 0
    while i < n:
        c = st[i]
        j = None
        if c == "'" or c == '"':
            j = quoted_end(st, i)
        elif c == "$":
            j = dollar_end(st, i)
        if j is None:
            i += 1
            continue
        res.append(re.sub(r"\s+", " ", st[start:i]))
        res.append(st[i:j])
        i = start = j
    res.append(re.sub(r"\s+", " ", st[start:]))
    return "".join(res).strip()


def load(path):
    items = set()
    for raw in statements(open(path).read()):
        st = squeeze(raw)
        if not st:
            continue
        if st.startswith("SET ") or "pg_catalog.set_config" in st or st.startswith("SELECT"):
            continue
        if re.search(r"vehicle_state_y\d{4}_\d{2}", st):
            continue
        if st.startswith("INSERT INTO"):  # a seed row's updated_at is the replay's clock
            st = re.sub(r"'\d{4}-\d\d-\d\d \d\d:\d\d:\d\d(\.\d+)?\+00'", "'<ts>'", st)
        m = re.match(r"CREATE TABLE (\S+) \((.*)\)(.*)$", st)
        if not m:
            items.add(st)
            continue
        table, body, opts = m.group(1), m.group(2), m.group(3).strip()
        parts, depth, cur = [], 0, ""
        for ch in body:
            if ch == "(":
                depth += 1
            elif ch == ")":
                depth -= 1
            if ch == "," and depth == 0:
                parts.append(cur.strip())
                cur = ""
            else:
                cur += ch
        parts.append(cur.strip())
        items.update(f"COL {table}: {p}" for p in parts)
        if opts:
            items.add(f"TBLOPT {table}: {opts}")
    return items


def main(a, b):
    left, right = load(a), load(b)
    if not left or not right:
        print("migration-dump-norm: an input has no statements", file=sys.stderr)
        return 1
    for x in sorted(left - right):
        print("-", x[:400])
    for x in sorted(right - left):
        print("+", x[:400])
    return 0 if left == right else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1], sys.argv[2]))
