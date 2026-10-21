#!/usr/bin/env python3
"""Revokes the Apple Development certificates this CI run minted.

Archiving with automatic signing on a runner without a development identity
makes Xcode create a new development certificate (its private key lives only
in the runner's keychain), and Apple caps how many a team may hold
(ios-v0.1.4 hit it). Distribution signing is Apple's cloud-managed
certificate and is not affected.

Revoking is irreversible, so (DA):
- only serials that appeared in the keychain during this run are revoked
  (`snapshot` before the archive, `revoke` after), so a reused runner never
  touches a certificate it didn't make;
- each serial is looked up on its own and revoked only when the lookup
  returns exactly one DEVELOPMENT / IOS_DEVELOPMENT certificate; there is no
  list-and-delete;
- no new serial means no revoke call;
- the API token is masked in the log; serials are logged by suffix only.

usage:
  revoke-runner-dev-certs.py snapshot <file>
  revoke-runner-dev-certs.py revoke <snapshot file> <AuthKey.p8> <key id> <issuer id>
Standard library plus the openssl, security and curl command-line tools only.
"""
import base64
import json
import os
import subprocess
import sys
import time
import urllib.parse

API = "https://api.appstoreconnect.apple.com/v1"
DEV_TYPES = ("DEVELOPMENT", "IOS_DEVELOPMENT")


def b64url(b: bytes) -> str:
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def der_to_raw(der: bytes) -> bytes:
    """ECDSA signature: DER SEQUENCE{INTEGER r, INTEGER s} -> r||s (32 bytes each)."""
    assert der[0] == 0x30
    i = 2 if der[1] < 0x80 else 2 + (der[1] & 0x7F)
    out = b""
    for _ in range(2):
        assert der[i] == 0x02
        n = der[i + 1]
        v = der[i + 2 : i + 2 + n].lstrip(b"\x00")
        out += v.rjust(32, b"\x00")
        i += 2 + n
    return out


def token(key_path: str, key_id: str, issuer: str) -> str:
    now = int(time.time())
    header = b64url(json.dumps({"alg": "ES256", "kid": key_id, "typ": "JWT"}).encode())
    claims = b64url(json.dumps({"iss": issuer, "iat": now, "exp": now + 600, "aud": "appstoreconnect-v1"}).encode())
    signing_input = f"{header}.{claims}".encode()
    der = subprocess.run(["openssl", "dgst", "-sha256", "-sign", key_path], input=signing_input,
                         capture_output=True, check=True).stdout
    jwt = f"{header}.{claims}.{b64url(der_to_raw(der))}"
    if os.environ.get("GITHUB_ACTIONS"):
        print(f"::add-mask::{jwt}", flush=True)
    return jwt


def api(method: str, path: str, jwt: str):
    # The system curl (macOS trust store); the token goes in on stdin, never argv.
    config = f'header = "Authorization: Bearer {jwt}"\n'
    out = subprocess.run(["curl", "-sS", "--fail-with-body", "--max-time", "30", "-X", method, "-K", "-", API + path],
                         input=config, capture_output=True, text=True)
    if out.returncode != 0:
        raise RuntimeError(f"{method} {path.split('?')[0]}: curl exit {out.returncode}: {out.stdout[:300]} {out.stderr[:200]}")
    return json.loads(out.stdout) if out.stdout.strip() else None


def norm(serial: str) -> str:
    return serial.strip().upper().lstrip("0")


def tail(serial: str) -> str:
    return "…" + serial[-6:]


def keychain_serials() -> dict:
    """Normalized serial -> serial as openssl prints it, for every 'Apple Development:'
    certificate in the runner's default keychain."""
    keychain = subprocess.run(["security", "default-keychain"], capture_output=True, text=True).stdout.strip().strip('"')
    pems = subprocess.run(["security", "find-certificate", "-a", "-c", "Apple Development:", "-p", keychain],
                          capture_output=True, text=True).stdout
    return serials_of(pems)


def serials_of(pems: str) -> dict:
    serials = {}
    for block in pems.split("-----END CERTIFICATE-----"):
        if "BEGIN CERTIFICATE" not in block:
            continue
        pem = block + "-----END CERTIFICATE-----\n"
        out = subprocess.run(["openssl", "x509", "-noout", "-serial"], input=pem,
                             capture_output=True, text=True, check=True).stdout
        raw = out.split("=", 1)[1].strip().upper()
        serials[norm(raw)] = raw
    return serials


def lookup(serial_forms: list, jwt: str) -> list:
    """The certificates whose serial is exactly one of the given spellings."""
    for form in dict.fromkeys(serial_forms):
        q = urllib.parse.urlencode({"filter[serialNumber]": form, "fields[certificates]": "serialNumber,certificateType"})
        data = api("GET", f"/certificates?{q}", jwt)["data"]
        if data:
            return data
    return []


def revoke(snapshot: str, key_path: str, key_id: str, issuer: str) -> int:
    try:
        with open(snapshot) as f:
            before = set(json.load(f))
    except FileNotFoundError:
        print("no snapshot from before the archive: nothing to compare, nothing revoked")
        return 0
    now = keychain_serials()
    new = {s: raw for s, raw in now.items() if s not in before}
    print(f"development certificates in the keychain: {len(before)} before, {len(now)} after, {len(new)} new")
    jwt = token(key_path, key_id, issuer)
    failed = 0
    for s, raw in new.items():
        try:
            found = lookup([raw, s], jwt)
            if len(found) != 1:
                print(f"skip {tail(s)}: the lookup returned {len(found)} certificates, not exactly one")
                continue
            cert = found[0]
            kind = cert["attributes"]["certificateType"]
            if kind not in DEV_TYPES or norm(cert["attributes"]["serialNumber"]) != s:
                print(f"skip {tail(s)}: {kind}, not a development certificate with this serial")
                continue
            api("DELETE", f"/certificates/{urllib.parse.quote(cert['id'])}", jwt)
            print(f"revoked {tail(s)} ({kind})")
        except RuntimeError as e:
            failed += 1
            print(f"::error::revoking {tail(s)} failed: {e}")
    q = urllib.parse.urlencode({"filter[certificateType]": ",".join(DEV_TYPES), "limit": "200",
                                "fields[certificates]": "certificateType"})
    left = len(api("GET", f"/certificates?{q}", jwt)["data"])
    print(f"development certificates left on the team: {left}")
    return 1 if failed else 0


def main() -> int:
    if sys.argv[1:2] == ["snapshot"]:
        with open(sys.argv[2], "w") as f:
            json.dump(sorted(keychain_serials()), f)
        return 0
    if sys.argv[1:2] == ["revoke"]:
        return revoke(*sys.argv[2:6])
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main())
