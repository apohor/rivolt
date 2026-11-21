#!/usr/bin/env python3
"""Which Apple team and bundle ids is the iPhone build tied to? Prints no secret.

preflight <AuthKey.p8> <key id> <issuer id> <expected team id> <bundle id> [...]
    Asks App Store Connect, with the CI API key, which team owns each bundle
    id (the bundle id resource's seedId is the Team ID) and compares it with
    the APPLE_TEAM_ID secret. Exit 1 only when the answer is definite and
    wrong: the key is refused (401), or a bundle id belongs to another team.
    Anything unclear (no network, a role or agreement error, a bundle id not
    registered yet) is a warning and exit 0, so this never blocks a build the
    archive step would have signed. Run it before the archive.

inspect <Rivolt.xcarchive | Rivolt.app> [expected team id]
    Prints, for the app and each extension in a signed build, the bundle id,
    the Team ID it is signed with, and the entitlements that are tied to the
    team (push environment, app groups, keychain groups, associated domains).
    Exit 1 when the expected team differs, or when the app and its extensions
    were signed by different teams. An unsigned build prints "unsigned".

Standard library plus the openssl, codesign and curl command-line tools.
"""
import base64
import glob
import json
import os
import plistlib
import subprocess
import sys
import time
import urllib.parse

API = "https://api.appstoreconnect.apple.com/v1"
TEAM_ENTITLEMENTS = (
    "application-identifier",
    "com.apple.developer.team-identifier",
    "aps-environment",
    "com.apple.security.application-groups",
    "keychain-access-groups",
    "com.apple.developer.associated-domains",
)


def b64url(b: bytes) -> str:
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def der_to_raw(der: bytes) -> bytes:
    """ECDSA signature: DER SEQUENCE{INTEGER r, INTEGER s} -> r||s (32 bytes each)."""
    i = 2 if der[1] < 0x80 else 2 + (der[1] & 0x7F)
    out = b""
    for _ in range(2):
        n = der[i + 1]
        out += der[i + 2 : i + 2 + n].lstrip(b"\x00").rjust(32, b"\x00")
        i += 2 + n
    return out


def token(key_path: str, key_id: str, issuer: str) -> str:
    now = int(time.time())
    header = b64url(json.dumps({"alg": "ES256", "kid": key_id, "typ": "JWT"}).encode())
    claims = b64url(json.dumps({"iss": issuer, "iat": now, "exp": now + 300, "aud": "appstoreconnect-v1"}).encode())
    signing_input = f"{header}.{claims}".encode()
    der = subprocess.run(["openssl", "dgst", "-sha256", "-sign", key_path], input=signing_input,
                         capture_output=True, check=True).stdout
    jwt = f"{header}.{claims}.{b64url(der_to_raw(der))}"
    if os.environ.get("GITHUB_ACTIONS"):
        print(f"::add-mask::{jwt}", flush=True)
    return jwt


def get(path: str, jwt: str):
    """(http status, parsed JSON or None). Status 0 means curl itself failed."""
    config = f'header = "Authorization: Bearer {jwt}"\n'  # on stdin, never argv
    out = subprocess.run(["curl", "-sS", "--max-time", "30", "-w", "\n%{http_code}", "-K", "-", API + path],
                         input=config, capture_output=True, text=True)
    if out.returncode != 0:
        return 0, None
    body, _, code = out.stdout.rpartition("\n")
    try:
        return int(code), (json.loads(body) if body.strip() else None)
    except ValueError:
        return int(code) if code.isdigit() else 0, None


def apple_errors(doc) -> str:
    """Apple's error codes and titles (never the detail, which can echo input)."""
    errs = (doc or {}).get("errors") or []
    return "; ".join(f"{e.get('code', '?')}: {e.get('title', '')}" for e in errs[:3])


def classify(status: int, doc, bundle: str, expected_team: str):
    """-> (level, message); level is ok, warn or fail."""
    if status == 0:
        return "warn", f"{bundle}: App Store Connect not reachable; team not checked"
    if status == 401:
        return "fail", (f"{bundle}: App Store Connect refused the API key ({apple_errors(doc) or '401'}). The key is revoked, "
                        "the key id or issuer id is wrong, or it belongs to a team whose membership is not active: check "
                        "the ASC_KEY_ID, ASC_ISSUER_ID and ASC_KEY_P8 secrets against Users and Access > Integrations.")
    if status != 200:
        return "warn", (f"{bundle}: App Store Connect answered {status} ({apple_errors(doc) or 'no detail'}); "
                        "team not checked. A missing agreement or an insufficient key role looks like this.")
    for item in (doc or {}).get("data", []):
        attrs = item.get("attributes", {})
        if attrs.get("identifier") != bundle:
            continue
        seed = attrs.get("seedId")
        if not seed:
            return "warn", f"{bundle}: registered under this key's team, but App Store Connect gave no Team ID"
        if seed == expected_team:
            return "ok", f"{bundle}: registered under the team in APPLE_TEAM_ID ({seed})"
        return "fail", (f"{bundle}: registered under team {seed}, but APPLE_TEAM_ID is {expected_team}. The key's team is not "
                        "the secret's team: set APPLE_TEAM_ID to the team that owns the bundle ids (a new organization team "
                        "needs the app set up in it first; see IOS-SUBMISSION-READINESS.md).")
    return "warn", (f"{bundle}: not registered under this key's team. If the team changed, create the app and its bundle ids "
                    "in the new team; Xcode registers a missing bundle id itself only when signing succeeds.")


def preflight(argv) -> int:
    key, key_id, issuer, team, *bundles = argv
    if not bundles:
        print("usage: preflight <p8> <key id> <issuer> <team id> <bundle id>...", file=sys.stderr)
        return 2
    jwt = token(key, key_id, issuer)
    worst = 0
    for bundle in bundles:
        status, doc = get("/bundleIds?" + urllib.parse.urlencode({"filter[identifier]": bundle, "limit": 200}), jwt)
        level, msg = classify(status, doc, bundle, team)
        print({"ok": "ok:   ", "warn": "warn: ", "fail": "FAIL: "}[level] + msg)
        if os.environ.get("GITHUB_ACTIONS") and level != "ok":
            print(f"::{'error' if level == 'fail' else 'warning'}::{msg}")
        worst = max(worst, {"ok": 0, "warn": 0, "fail": 1}[level])
    return worst


def entitlements_of(path: str) -> dict:
    out = subprocess.run(["codesign", "-d", "--entitlements", ":-", path], capture_output=True)
    return plistlib.loads(out.stdout) if out.returncode == 0 and out.stdout.strip() else {}


def team_of(path: str):
    out = subprocess.run(["codesign", "-dv", path], capture_output=True, text=True)
    for line in out.stderr.splitlines():
        if line.startswith("TeamIdentifier="):
            v = line.split("=", 1)[1].strip()
            return None if v == "not set" else v
    return None


def describe(info: dict, ents: dict, team) -> dict:
    """What one signed bundle carries, reduced to the team-bound facts."""
    return {
        "bundle_id": info.get("CFBundleIdentifier"),
        "version": f"{info.get('CFBundleShortVersionString', '?')} ({info.get('CFBundleVersion', '?')})",
        "team": team,
        "entitlements": {k: ents[k] for k in TEAM_ENTITLEMENTS if k in ents},
    }


def verdict(bundles: list, expected_team=None):
    """-> list of problems (empty is good)."""
    problems = []
    teams = {b["team"] for b in bundles}
    if None in teams:
        problems.append("unsigned: no Team ID on " + ", ".join(b["bundle_id"] or "?" for b in bundles if not b["team"]))
    signed = teams - {None}
    if len(signed) > 1:
        problems.append("the app and its extensions are signed by different teams: " + ", ".join(sorted(signed)))
    if expected_team and signed and signed != {expected_team}:
        problems.append(f"signed by {', '.join(sorted(signed))}, expected {expected_team}")
    return problems


def inspect(argv) -> int:
    path, expected = argv[0], (argv[1] if len(argv) > 1 else None)
    app = path
    if path.rstrip("/").endswith(".xcarchive"):
        apps = glob.glob(os.path.join(path, "Products", "Applications", "*.app"))
        if len(apps) != 1:
            print(f"no single .app in {path}", file=sys.stderr)
            return 2
        app = apps[0]
    targets = [app] + sorted(glob.glob(os.path.join(app, "PlugIns", "*.appex")))
    bundles = []
    for t in targets:
        with open(os.path.join(t, "Info.plist"), "rb") as f:
            info = plistlib.load(f)
        bundles.append(describe(info, entitlements_of(t), team_of(t)))
    for b in bundles:
        print(f"{b['bundle_id']}  {b['version']}  team {b['team'] or 'unsigned'}")
        for k, v in b["entitlements"].items():
            print(f"    {k}: {v}")
    problems = verdict(bundles, expected)
    for p in problems:
        print("FAIL: " + p)
    return 1 if problems else 0


def main(argv) -> int:
    if len(argv) >= 2 and argv[0] == "preflight" and len(argv) >= 6:
        return preflight(argv[1:])
    if len(argv) >= 2 and argv[0] == "inspect":
        return inspect(argv[1:])
    print(__doc__, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
