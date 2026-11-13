#!/usr/bin/env python3
"""migration-dump-norm.py A B: compare two pg_dump files as sets of statements; exit 1 and print the difference when they differ.

Why sets of normalised statements and not diff: pg_dump orders objects by dependency, and that order moves when a table is created by a
different route (CREATE then ALTER versus one CREATE). What must be equal is the schema, the seed rows and the grants, not the order they
are written in. A CREATE TABLE is split into one item per column and constraint, so a column order or a default shows as a single line.

Skipped: comments, psql meta lines, the clock in seed rows (read as <ts>), SET/set_config/SELECT preamble lines, and the vehicle_state_yYYYY_MM partitions, which the migration that creates
the partitioned table makes relative to the day it runs (a dump on another day names other months).
"""
import re
import sys


def load(path):
    text = re.sub(r"--[^\n]*", "", open(path).read())
    text = re.sub(r"(?m)^\\[^\n]*\n", "", text)  # psql meta lines (\restrict TOKEN): random per dump
    text = re.sub(r"'\d{4}-\d\d-\d\d \d\d:\d\d:\d\d(\.\d+)?\+00'", "'<ts>'", text)  # a seed row's updated_at is the replay's clock
    items = set()
    for st in (x.strip() for x in re.split(r";\n", text)):
        if not st:
            continue
        st = re.sub(r"\s+", " ", st)
        if st.startswith("SET ") or "pg_catalog.set_config" in st or st.startswith("SELECT"):
            continue
        if re.search(r"vehicle_state_y\d{4}_\d{2}", st):
            continue
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
