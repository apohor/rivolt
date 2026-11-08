#!/usr/bin/env python3
"""Generates ios/Rivolt/Theme/ChargerLabels.swift from ios/design/charger-labels.json.

charger-labels.json is generated from the web (web/src/lib/chargerLabels.json: UPDATE_CHARGER_LABELS=1 npx vitest run
src/lib/chargerLabels.test.ts); this turns it into Swift so no charger word or explanation is typed twice. Run from the repo root
after a deliberate change to the web's strings:

    python3 ios/design/generate_charger_labels.py

ChargerLabelsTests fails when the committed Swift and charger-labels.json disagree.
"""
import json
import pathlib

here = pathlib.Path(__file__).resolve().parent
doc = json.loads((here / "charger-labels.json").read_text())
out = here.parent / "Rivolt" / "Theme" / "ChargerLabels.swift"


def swift(s: str) -> str:
    return json.dumps(s, ensure_ascii=False)


kinds = list(doc["types"].keys())
lines = [
    "// Generated from ios/design/charger-labels.json by ios/design/generate_charger_labels.py. Do not edit by hand:",
    "// change web/src/lib/chargerLabels.json, regenerate charger-labels.json, then run the script. ChargerLabelsTests checks the two agree.",
    "import Foundation",
    "",
    "/// The charger types, one set of words for the whole app (and the web): Fast (DC), Medium (Level 2), Hotel, Camping, Outlet.",
    "enum ChargerKind: String, CaseIterable, Equatable {",
    "    case " + ", ".join(kinds),
    "}",
    "",
    "enum ChargerLabels {",
    f"    static let all = {swift(doc['all'])}",
    f"    static let infoLabel = {swift(doc['infoLabel'])}",
    "",
    "    /// The one-word label.",
    "    static func label(_ kind: ChargerKind) -> String {",
    "        switch kind {",
]
for k in kinds:
    lines.append(f"        case .{k}: return {swift(doc['types'][k]['label'])}")
lines += [
    "        }",
    "    }",
    "",
    "    /// The optional explanation: at most two short sentences, the fact first, then one caveat.",
    "    static func sentence(_ kind: ChargerKind) -> String {",
    "        switch kind {",
]
for k in kinds:
    lines.append(f"        case .{k}: return {swift(doc['types'][k]['sentence'])}")
lines += ["        }", "    }", "}", ""]
out.write_text("\n".join(lines))
print("wrote", out)
