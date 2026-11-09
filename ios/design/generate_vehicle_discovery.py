#!/usr/bin/env python3
"""Generates ios/Rivolt/Theme/VehicleDiscoveryCopy.swift from ios/design/vehicle-discovery.json.

vehicle-discovery.json is generated from the web (web/src/lib/vehicleDiscovery.json: UPDATE_VEHICLE_DISCOVERY=1 npx vitest run
src/lib/vehicleDiscovery.test.ts); this turns it into Swift so no word is typed twice. Run from the repo root after a deliberate change:

    python3 ios/design/generate_vehicle_discovery.py

VehicleDiscoveryTests fails when the committed Swift and vehicle-discovery.json disagree.
"""
import json
import pathlib

here = pathlib.Path(__file__).resolve().parent
doc = json.loads((here / "vehicle-discovery.json").read_text())
out = here.parent / "Rivolt" / "Theme" / "VehicleDiscoveryCopy.swift"


def swift(s: str) -> str:
    return json.dumps(s, ensure_ascii=False)


lines = [
    "// Generated from ios/design/vehicle-discovery.json by ios/design/generate_vehicle_discovery.py. Do not edit by hand:",
    "// change web/src/lib/vehicleDiscovery.json, regenerate vehicle-discovery.json, then run the script. VehicleDiscoveryTests checks the two agree.",
    "import Foundation",
    "",
    "/// What the app says while a linked Rivian account has no vehicle on file, one set of words with the web.",
    "enum VehicleDiscoveryCopy {",
    f"    static let lookingTitle = {swift(doc['looking']['title'])}",
    f"    static let lookingBody = {swift(doc['looking']['body'])}",
    f"    static let noneTitle = {swift(doc['none']['title'])}",
    f"    static let noneBody = {swift(doc['none']['body'])}",
    f"    static let reauthTitle = {swift(doc['reauth']['title'])}",
    f"    static let reauthBody = {swift(doc['reauth']['body'])}",
    f"    static let reauthAction = {swift(doc['reauth']['action'])}",
    f"    static let check = {swift(doc['check'])}",
    "}",
    "",
]
out.write_text("\n".join(lines))
print("wrote", out)
