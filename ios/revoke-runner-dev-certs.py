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

Then a sweep (the owner's request; DA's amendment to the first rule): any
development certificate named exactly "Created via API" (made by an API key,
so by CI runs, cancelled ones included, or any other API-key tooling) whose
notBefore is more than 2 hours ago. At most 5 per run: more matches mean the
filter is wrong, so none is revoked and the step fails. Log-only unless
RIVOLT_CERT_SWEEP=delete.

Then a one-time cleanup of a backlog the sweep refuses (more than 5): only
the certificates whose serials end in the listed suffixes (as the sweep's
log prints them), each of which must match exactly one old "Created via
API" development certificate, or nothing is revoked. Dry run unless
RIVOLT_CERT_CLEANUP=delete. Run by hand (.github/workflows/ios-dev-cert-cleanup.yml).

usage:
  revoke-runner-dev-certs.py snapshot <file>
  revoke-runner-dev-certs.py revoke <snapshot file> <AuthKey.p8> <key id> <issuer id>
  revoke-runner-dev-certs.py cleanup <AuthKey.p8> <key id> <issuer id> <suffix,suffix,...>
Standard library plus the openssl, security and curl command-line tools only.
"""
import base64
import calendar
import json
import os
import subprocess
import sys
import time
import urllib.parse

API = "https://api.appstoreconnect.apple.com/v1"
DEV_TYPES = ("DEVELOPMENT", "IOS_DEVELOPMENT")
SWEEP_NAME = "Created via API"
SWEEP_MIN_AGE = 2 * 3600
SWEEP_CAP = 5


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


class KeychainError(Exception):
    pass


def keychain_serials() -> dict:
    """Normalized serial -> serial as openssl prints it, for every 'Apple Development:'
    certificate in the runner's default keychain. Fails closed: a keychain that
    can't be read raises, it never reads as empty (an empty snapshot would make
    every certificate look new)."""
    kc = subprocess.run(["security", "default-keychain"], capture_output=True, text=True)
    keychain = kc.stdout.strip().strip('"')
    if kc.returncode != 0 or not keychain:
        raise KeychainError(f"security default-keychain: exit {kc.returncode}")
    found = subprocess.run(["security", "find-certificate", "-a", "-c", "Apple Development:", "-p", keychain],
                           capture_output=True, text=True)
    if found.returncode == 44:  # errSecItemNotFound: none in the keychain
        return {}
    if found.returncode != 0:
        raise KeychainError(f"security find-certificate: exit {found.returncode}")
    return serials_of(found.stdout)


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


def not_before(content_b64: str):
    """The certificate's notBefore as a Unix time, or None if it can't be read."""
    try:
        der = base64.b64decode(content_b64)
        out = subprocess.run(["openssl", "x509", "-inform", "DER", "-noout", "-startdate"], input=der,
                             capture_output=True, check=True).stdout.decode()
        return calendar.timegm(time.strptime(out.split("=", 1)[1].strip(), "%b %d %H:%M:%S %Y GMT"))
    except Exception:  # noqa: BLE001 - any parse failure means "skip"
        return None


def sweep(jwt: str, revoked: set, now: float, delete: bool) -> int:
    """Old API-made development certificates; see the module doc."""
    q = urllib.parse.urlencode({"filter[certificateType]": ",".join(DEV_TYPES), "limit": "200",
                                "fields[certificates]": "certificateType,displayName,serialNumber,expirationDate,certificateContent"})
    certs = api("GET", f"/certificates?{q}", jwt)["data"]
    candidates = []
    for c in certs:
        a = c["attributes"]
        s = norm(a.get("serialNumber") or "")
        # Never the certificate's name: it can carry a person's name, and the
        # CI log is public (DA). displayName is only printed when it is ours.
        line = f"{a.get('certificateType')} displayName={a.get('displayName')!r} expires={a.get('expirationDate')} {tail(s)}"
        if s in revoked:
            continue
        if a.get("certificateType") not in DEV_TYPES or a.get("displayName") != SWEEP_NAME:
            # No name in the log: it may be a person's, and the CI log is public.
            print(f"sweep keeps (not named {SWEEP_NAME!r}): {a.get('certificateType')} {tail(s)}")
            continue
        nb = not_before(a.get("certificateContent") or "")
        if nb is None:
            print(f"sweep skips (no readable notBefore): {line}")
            continue
        if now - nb < SWEEP_MIN_AGE:
            print(f"sweep skips (younger than 2 h): {line}")
            continue
        candidates.append((c["id"], line))
    print(f"sweep: {len(certs)} development certificates, {len(candidates)} old API-made")
    if len(candidates) > SWEEP_CAP:
        # List them anyway, so the log shows what the filter caught.
        for _, line in candidates:
            print(f"sweep matched (not revoked): {line}")
        print(f"::error::sweep matched {len(candidates)} (> {SWEEP_CAP}): the filter is wrong, nothing revoked")
        return 1
    for cid, line in candidates:
        if not delete:
            print(f"sweep would revoke (log-only): {line}")
            continue
        api("DELETE", f"/certificates/{urllib.parse.quote(cid)}", jwt)
        print(f"sweep revoked: {line}")
    return 0


def select_listed(certs: list, suffixes: list, now: float, nb_of=None):
    """(ids to revoke, problems) for the cleanup. Every suffix must match exactly
    one certificate that is a development certificate named SWEEP_NAME and
    older than SWEEP_MIN_AGE; any problem means the caller revokes nothing."""
    nb_of = nb_of or (lambda c: not_before(c["attributes"].get("certificateContent") or ""))
    picked, problems = [], []
    for suf in suffixes:
        suf = suf.strip().lstrip("…").upper()
        if len(suf) < 6 or any(ch not in "0123456789ABCDEF" for ch in suf):
            problems.append(f"{suf!r}: not a hex serial suffix of at least 6 characters")
            continue
        hits = [c for c in certs if norm(c["attributes"].get("serialNumber") or "").endswith(suf)]
        if len(hits) != 1:
            problems.append(f"…{suf}: matches {len(hits)} development certificates, not exactly one")
            continue
        c = hits[0]
        a = c["attributes"]
        if a.get("certificateType") not in DEV_TYPES or a.get("displayName") != SWEEP_NAME:
            problems.append(f"…{suf}: not a development certificate named {SWEEP_NAME!r}")
            continue
        nb = nb_of(c)
        if nb is None or now - nb < SWEEP_MIN_AGE:
            problems.append(f"…{suf}: younger than 2 h or no readable notBefore")
            continue
        picked.append(c)
    ids = [c["id"] for c in picked]
    if len(set(ids)) != len(ids):
        problems.append("two suffixes matched the same certificate")
    return ([] if problems else ids), problems


def cleanup(key_path: str, key_id: str, issuer: str, suffixes: str) -> int:
    wanted = [x for x in suffixes.replace(" ", ",").split(",") if x.strip()]
    if not wanted:
        print("::error::no serial suffixes given: nothing revoked")
        return 1
    jwt = token(key_path, key_id, issuer)
    q = urllib.parse.urlencode({"filter[certificateType]": ",".join(DEV_TYPES), "limit": "200",
                                "fields[certificates]": "certificateType,displayName,serialNumber,expirationDate,certificateContent"})
    certs = api("GET", f"/certificates?{q}", jwt)["data"]
    ids, problems = select_listed(certs, wanted, time.time())
    if problems:
        for p in problems:
            print(f"::error::cleanup: {p}")
        print(f"cleanup: {len(problems)} problem(s): nothing revoked")
        return 1
    delete = os.environ.get("RIVOLT_CERT_CLEANUP") == "delete"
    by_id = {c["id"]: c for c in certs}
    for cid in ids:
        a = by_id[cid]["attributes"]
        line = f"{a.get('certificateType')} displayName={a.get('displayName')!r} expires={a.get('expirationDate')} {tail(norm(a.get('serialNumber') or ''))}"
        if not delete:
            print(f"cleanup would revoke (dry run): {line}")
            continue
        api("DELETE", f"/certificates/{urllib.parse.quote(cid)}", jwt)
        print(f"cleanup revoked: {line}")
    left = len(api("GET", f"/certificates?{q}", jwt)["data"])
    print(f"cleanup: {len(ids)} {'revoked' if delete else 'would be revoked'}; development certificates on the team now: {left}")
    return 0


def revoke(snapshot: str, key_path: str, key_id: str, issuer: str) -> int:
    try:
        with open(snapshot) as f:
            before = set(json.load(f))
    except FileNotFoundError:
        print("no snapshot from before the archive: nothing to compare, nothing revoked")
        return 0
    try:
        now = keychain_serials()
    except KeychainError as e:
        print(f"::error::can't read the keychain after the archive ({e}): nothing revoked")
        return 1
    new = {s: raw for s, raw in now.items() if s not in before}
    print(f"development certificates in the keychain: {len(before)} before, {len(now)} after, {len(new)} new")
    jwt = token(key_path, key_id, issuer)
    failed = 0
    revoked = set()
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
            revoked.add(s)
            print(f"revoked {tail(s)} ({kind})")
        except RuntimeError as e:
            failed += 1
            print(f"::error::revoking {tail(s)} failed: {e}")
    try:
        failed += sweep(jwt, revoked, time.time(), os.environ.get("RIVOLT_CERT_SWEEP") == "delete")
    except RuntimeError as e:
        failed += 1
        print(f"::error::sweep failed: {e}")
    q = urllib.parse.urlencode({"filter[certificateType]": ",".join(DEV_TYPES), "limit": "200",
                                "fields[certificates]": "certificateType"})
    left = len(api("GET", f"/certificates?{q}", jwt)["data"])
    print(f"development certificates left on the team: {left}")
    return 1 if failed else 0


def main() -> int:
    if sys.argv[1:2] == ["snapshot"]:
        try:
            serials = keychain_serials()
        except KeychainError as e:
            # No snapshot file: the revoke step then revokes nothing.
            print(f"::error::can't read the keychain before the archive ({e})")
            return 1
        with open(sys.argv[2], "w") as f:
            json.dump(sorted(serials), f)
        return 0
    if sys.argv[1:2] == ["revoke"]:
        return revoke(*sys.argv[2:6])
    if sys.argv[1:2] == ["cleanup"] and len(sys.argv) == 6:
        return cleanup(*sys.argv[2:6])
    print(__doc__)
    return 2


if __name__ == "__main__":
    sys.exit(main())
