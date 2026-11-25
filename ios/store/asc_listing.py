#!/usr/bin/env python3
"""Applies ios/store/listing.md and ios/store/privacy-label.md to App Store Connect through its API (GitOps for the listing).

  asc_listing.py plan|apply [--version 1.4.3] [--shots-dir DIR] [--listing PATH]

`plan` (the workflow's default) reads from App Store Connect and prints every write it would make; it writes nothing.
`apply` makes those writes. Neither submits the version for review: submission stays a separate, explicit action.
App Privacy has no public API, so both modes print the click list for the owner from privacy-label.md.

Secrets come from the environment and are never printed: ASC_KEY_P8 (key text), ASC_KEY_ID, ASC_ISSUER_ID, and for the
review details ASC_REVIEW_USER, ASC_REVIEW_PASSWORD, ASC_REVIEW_CONTACT_FIRST_NAME, ASC_REVIEW_CONTACT_LAST_NAME,
ASC_REVIEW_CONTACT_PHONE, ASC_REVIEW_CONTACT_EMAIL. The password is masked in the plan.

Standard library plus the openssl command-line tool. Not seen: a run against the live API (payloads follow Apple's
documented resource names; the first `plan` run is the check). The payload builders are pure and unit-tested with a
fake client (test_asc_listing.py).
"""
import argparse
import base64
import hashlib
import importlib.util
import json
import os
import re
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
API = "https://api.appstoreconnect.apple.com"
BUNDLE_ID = "dev.rivolt.app"
LOCALE = "en-US"
COPYRIGHT = "2026 ZERODOWNTIME LLC"
SHOT_ORDER = ["now", "money", "drive", "charge", "truck", "trust"]  # the caption order in listing.md
SHOT_MODE = "dark"  # dark is the primary set (listing.md)
# Size folder -> App Store Connect display type. The 6.3 mapping is the one we have not seen Apple accept: plan prints it.
DISPLAY_TYPES = {"6.9": "APP_IPHONE_69", "6.3": "APP_IPHONE_61"}
AGE_NONE = {
    "alcoholTobaccoOrDrugUseOrReferences": "NONE", "contests": "NONE", "gamblingSimulated": "NONE",
    "horrorOrFearThemes": "NONE", "matureOrSuggestiveThemes": "NONE", "medicalOrTreatmentInformation": "NONE",
    "profanityOrCrudeHumor": "NONE", "sexualContentGraphicAndNudity": "NONE", "sexualContentOrNudity": "NONE",
    "violenceCartoonOrFantasy": "NONE", "violenceRealistic": "NONE", "violenceRealisticProlongedGraphicOrSadistic": "NONE",
    "gambling": False, "unrestrictedWebAccess": False,
}
SECRET_KEYS = {"demoAccountPassword", "demoAccountName", "contactEmail", "contactPhone"}


def _load(name):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


# ---------- payload builders (pure) ----------

def rel(kind, id_):
    return {"data": {"type": kind, "id": id_}}


def version_localization_attrs(s, first_version):
    """The en-US store text. App Store Connect has no What's New on a first version, so it is left out there."""
    a = {"description": s["Description"], "keywords": s["Keywords"], "promotionalText": s["Promotional text"],
         "supportUrl": s["Support URL"], "marketingUrl": s["Marketing URL"]}
    if not first_version:
        a["whatsNew"] = s["What's New"]
    return a


def version_attrs():
    return {"copyright": COPYRIGHT, "releaseType": "MANUAL"}


def app_info_localization_attrs(s):
    return {"subtitle": s["Subtitle"], "privacyPolicyUrl": s["Privacy Policy URL"]}


def app_info_rel():
    return {"primaryCategory": rel("appCategories", "UTILITIES"), "secondaryCategory": rel("appCategories", "TRAVEL")}


def review_detail_attrs(s, env):
    need = ["ASC_REVIEW_USER", "ASC_REVIEW_PASSWORD", "ASC_REVIEW_CONTACT_FIRST_NAME", "ASC_REVIEW_CONTACT_LAST_NAME",
            "ASC_REVIEW_CONTACT_PHONE", "ASC_REVIEW_CONTACT_EMAIL"]
    missing = [n for n in need if not env.get(n)]
    if missing:
        raise SystemExit("missing secrets (by name): " + ", ".join(missing))
    return {"contactFirstName": env["ASC_REVIEW_CONTACT_FIRST_NAME"], "contactLastName": env["ASC_REVIEW_CONTACT_LAST_NAME"],
            "contactPhone": env["ASC_REVIEW_CONTACT_PHONE"], "contactEmail": env["ASC_REVIEW_CONTACT_EMAIL"],
            "demoAccountRequired": True, "demoAccountName": env["ASC_REVIEW_USER"],
            "demoAccountPassword": env["ASC_REVIEW_PASSWORD"], "notes": s["Review notes"]}


def availability_payload(app_id):
    return {"data": {"type": "appAvailabilities", "attributes": {"availableInNewTerritories": False},
                     "relationships": {"app": rel("apps", app_id),
                                       "territoryAvailabilities": {"data": [{"type": "territoryAvailabilities", "id": "${usa}"}]}}},
            "included": [{"type": "territoryAvailabilities", "id": "${usa}", "attributes": {"available": True},
                          "relationships": {"territory": rel("territories", "USA")}}]}


def price_schedule_payload(app_id, free_point_id):
    return {"data": {"type": "appPriceSchedules",
                     "relationships": {"app": rel("apps", app_id), "baseTerritory": rel("territories", "USA"),
                                       "manualPrices": {"data": [{"type": "appPrices", "id": "${free}"}]}}},
            "included": [{"type": "appPrices", "id": "${free}", "attributes": {"startDate": None},
                          "relationships": {"appPricePoint": rel("appPricePoints", free_point_id)}}]}


def screenshot_files(shots_dir):
    """{size: [paths in caption order]} from <shots_dir>/<size>/<name>-dark.png; raises on a missing file."""
    out = {}
    for size in DISPLAY_TYPES:
        paths = [os.path.join(shots_dir, size, f"{n}-{SHOT_MODE}.png") for n in SHOT_ORDER]
        gone = [p for p in paths if not os.path.isfile(p)]
        if gone:
            raise SystemExit("missing store shots: " + ", ".join(os.path.relpath(p, shots_dir) for p in gone))
        out[size] = paths
    return out


def click_list(privacy_md):
    """The owner's click list for App Privacy (no public API) from privacy-label.md's seven-row table."""
    rows = []
    for line in privacy_md.splitlines():
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) >= 3 and line.startswith("|") and not set(line) <= set("|- ") and cells[0] != "App Store Connect category":
            rows.append((cells[0], cells[1]))
    if len(rows) != 7:
        raise SystemExit(f"privacy-label.md: expected 7 data types, found {len(rows)}")
    out = ["App Privacy (do by hand in App Store Connect > the app > App Privacy; no public API):",
           "  1. 'Do you or your third-party partners collect data from this app?' -> Yes."]
    for i, (cat, typ) in enumerate(rows, 2):
        out.append(f"  {i}. Tick {cat} > {typ}: Linked to the user = Yes; Used for tracking = No; Purpose = App Functionality only.")
    out.append(f"  {len(rows) + 2}. Tracking question -> No. Publish the label. Everything else stays not collected.")
    return out


# ---------- the App Store Connect client ----------

class Api:
    """JSON over HTTPS with a short-lived ES256 token. The token and key never reach the output."""

    def __init__(self, key_text, key_id, issuer):
        self.key_text, self.key_id, self.issuer = key_text, key_id, issuer

    def _token(self):
        b64 = lambda b: base64.urlsafe_b64encode(b).rstrip(b"=").decode()
        now = int(time.time())
        head = b64(json.dumps({"alg": "ES256", "kid": self.key_id, "typ": "JWT"}).encode())
        claims = b64(json.dumps({"iss": self.issuer, "iat": now, "exp": now + 600, "aud": "appstoreconnect-v1"}).encode())
        with tempfile.NamedTemporaryFile("w", delete=False) as f:
            os.chmod(f.name, 0o600)
            f.write(self.key_text + "\n")
        try:
            der = subprocess.run(["openssl", "dgst", "-sha256", "-sign", f.name], input=f"{head}.{claims}".encode(),
                                 capture_output=True, check=True).stdout
        finally:
            os.unlink(f.name)
        i = 2 if der[1] < 0x80 else 2 + (der[1] & 0x7F)
        raw = b""
        for _ in range(2):
            n = der[i + 1]
            raw += der[i + 2:i + 2 + n].lstrip(b"\x00").rjust(32, b"\x00")
            i += 2 + n
        jwt = f"{head}.{claims}.{b64(raw)}"
        if os.environ.get("GITHUB_ACTIONS"):
            print(f"::add-mask::{jwt}", flush=True)
        return jwt

    def request(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(API + path, data=data, method=method,
                                     headers={"Authorization": "Bearer " + self._token(), "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req, timeout=60) as r:
                raw = r.read()
                return r.status, (json.loads(raw) if raw else {})
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                return e.code, json.loads(raw)
            except ValueError:
                return e.code, {}

    def put_bytes(self, url, headers, chunk):
        req = urllib.request.Request(url, data=chunk, method="PUT", headers=dict(headers))
        with urllib.request.urlopen(req, timeout=120) as r:
            return r.status


def errors_of(doc):
    return "; ".join(f"{e.get('code', '')} {e.get('title', '')}: {e.get('detail', '')}" for e in (doc or {}).get("errors", []))[:400]


# ---------- the run ----------

NEW = "(new)"


class Run:
    def __init__(self, api, dry, out=print):
        self.api, self.dry, self.out = api, dry, out
        self.writes = []

    def get(self, path):
        status, doc = self.api.request("GET", path)
        if status != 200:
            raise SystemExit(f"GET {path} -> {status} {errors_of(doc)}")
        return doc

    def write(self, method, path, body=None, note=""):
        shown = json.loads(json.dumps(body)) if body is not None else None
        for node in ([shown.get("data")] if shown else []):
            attrs = (node or {}).get("attributes", {}) if isinstance(node, dict) else {}
            for k in SECRET_KEYS & set(attrs):
                attrs[k] = "***"
        self.writes.append((method, path, shown))
        self.out(f"  {'WOULD ' if self.dry else ''}{method} {path} {note}".rstrip())
        if shown is not None:
            self.out("    " + json.dumps(shown, ensure_ascii=False)[:600])
        if self.dry:
            return NEW, {}
        status, doc = self.api.request(method, path, body)
        if status >= 300:
            raise SystemExit(f"{method} {path} -> {status} {errors_of(doc)}")
        return (doc.get("data") or {}).get("id", ""), doc


def run(api, mode, listing_md, privacy_md, version, shots_dir, env, out=print):
    s = _load("check_listing").sections(listing_md)
    problems = _load("check_listing").check(listing_md)
    if problems:
        raise SystemExit("listing.md fails its checks: " + "; ".join(problems))
    r = Run(api, mode != "apply", out)
    out(f"{'PLAN (nothing is written)' if r.dry else 'APPLY'} for {BUNDLE_ID} version {version}")

    docs = r.get(f"/v1/apps?filter[bundleId]={BUNDLE_ID}")["data"]
    if len(docs) != 1:
        raise SystemExit(f"expected one app for {BUNDLE_ID}, found {len(docs)}")
    app = docs[0]["id"]

    builds = r.get(f"/v1/builds?filter[app]={app}&filter[preReleaseVersion.version]={version}&sort=-uploadedDate&limit=1")["data"]
    if not builds or builds[0]["attributes"].get("processingState") != "VALID":
        raise SystemExit(f"no processed (VALID) build for {version}: upload it first")
    build = builds[0]["id"]
    out("content rights, availability, price")
    r.write("PATCH", f"/v1/apps/{app}", {"data": {"type": "apps", "id": app, "attributes": {"contentRightsDeclaration": "USES_THIRD_PARTY_CONTENT"}}})
    r.write("POST", "/v2/appAvailabilities", availability_payload(app), "United States only")
    points = r.get(f"/v1/apps/{app}/appPricePoints?filter[territory]=USA&limit=200")["data"]
    free = [p["id"] for p in points if p.get("attributes", {}).get("customerPrice") in ("0", "0.0", "0.00")]
    if len(free) != 1:
        raise SystemExit(f"expected one free USA price point, found {len(free)}")
    r.write("POST", "/v1/appPriceSchedules", price_schedule_payload(app, free[0]), "free")

    out("version")
    versions = r.get(f"/v1/apps/{app}/appStoreVersions?filter[platform]=IOS&limit=200")["data"]
    mine = [v for v in versions if v["attributes"]["versionString"] == version]
    first = len(versions) == len([v for v in mine])  # no other version exists: a first release has no What's New
    if mine:
        ver = mine[0]["id"]
        r.write("PATCH", f"/v1/appStoreVersions/{ver}", {"data": {"type": "appStoreVersions", "id": ver, "attributes": version_attrs()}})
    else:
        ver, _ = r.write("POST", "/v1/appStoreVersions", {"data": {"type": "appStoreVersions", "attributes": dict(version_attrs(), platform="IOS", versionString=version),
                                                                  "relationships": {"app": rel("apps", app)}}}, "create the version")
    if ver != NEW:
        r.write("PATCH", f"/v1/appStoreVersions/{ver}", {"data": {"type": "appStoreVersions", "id": ver, "relationships": {"build": rel("builds", build)}}}, "attach the build")
    else:
        out(f"  WOULD attach build {build} to the new version")
    r.write("PATCH", f"/v1/builds/{build}", {"data": {"type": "builds", "id": build, "attributes": {"usesNonExemptEncryption": False}}}, "export compliance")

    out("version text (en-US)")
    loc = None
    if ver != NEW:
        locs = r.get(f"/v1/appStoreVersions/{ver}/appStoreVersionLocalizations")["data"]
        loc = next((x["id"] for x in locs if x["attributes"]["locale"] == LOCALE), None)
    attrs = version_localization_attrs(s, first)
    if first:
        out("  What's New is not sent: App Store Connect has none on a first version")
    if loc:
        r.write("PATCH", f"/v1/appStoreVersionLocalizations/{loc}", {"data": {"type": "appStoreVersionLocalizations", "id": loc, "attributes": attrs}})
    else:
        loc, _ = r.write("POST", "/v1/appStoreVersionLocalizations", {"data": {"type": "appStoreVersionLocalizations", "attributes": dict(attrs, locale=LOCALE),
                                                                                "relationships": {"appStoreVersion": rel("appStoreVersions", ver)}}})

    out("app info: subtitle, privacy URL, categories, age rating")
    infos = r.get(f"/v1/apps/{app}/appInfos")["data"]
    editable = [i for i in infos if i["attributes"].get("appStoreState") in ("PREPARE_FOR_SUBMISSION", "DEVELOPER_REJECTED", "REJECTED", "METADATA_REJECTED", "WAITING_FOR_REVIEW", "INVALID_BINARY")] or infos
    info = editable[0]["id"]
    r.write("PATCH", f"/v1/appInfos/{info}", {"data": {"type": "appInfos", "id": info, "relationships": app_info_rel()}})
    iloc = next((x["id"] for x in r.get(f"/v1/appInfos/{info}/appInfoLocalizations")["data"] if x["attributes"]["locale"] == LOCALE), None)
    if iloc:
        r.write("PATCH", f"/v1/appInfoLocalizations/{iloc}", {"data": {"type": "appInfoLocalizations", "id": iloc, "attributes": app_info_localization_attrs(s)}})
    else:
        r.write("POST", "/v1/appInfoLocalizations", {"data": {"type": "appInfoLocalizations", "attributes": dict(app_info_localization_attrs(s), locale=LOCALE),
                                                              "relationships": {"appInfo": rel("appInfos", info)}}})
    age = r.get(f"/v1/appInfos/{info}/ageRatingDeclaration")["data"]["id"]
    r.write("PATCH", f"/v1/ageRatingDeclarations/{age}", {"data": {"type": "ageRatingDeclarations", "id": age, "attributes": AGE_NONE}}, "all none (4+)")

    out("review details")
    rattrs = review_detail_attrs(s, env)
    detail = None
    if ver != NEW:
        d = r.get(f"/v1/appStoreVersions/{ver}/appStoreReviewDetail").get("data")
        detail = d["id"] if d else None
    if detail:
        r.write("PATCH", f"/v1/appStoreReviewDetails/{detail}", {"data": {"type": "appStoreReviewDetails", "id": detail, "attributes": rattrs}})
    else:
        r.write("POST", "/v1/appStoreReviewDetails", {"data": {"type": "appStoreReviewDetails", "attributes": rattrs,
                                                               "relationships": {"appStoreVersion": rel("appStoreVersions", ver)}}})

    out("screenshots (replace the existing sets)")
    files = screenshot_files(shots_dir)
    for size, paths in files.items():
        dtype = DISPLAY_TYPES[size]
        set_id = None
        if loc and loc != NEW:
            for st in r.get(f"/v1/appStoreVersionLocalizations/{loc}/appScreenshotSets")["data"]:
                if st["attributes"]["screenshotDisplayType"] == dtype:
                    set_id = st["id"]
        if set_id:
            for sh in r.get(f"/v1/appScreenshotSets/{set_id}/appScreenshots")["data"]:
                r.write("DELETE", f"/v1/appScreenshots/{sh['id']}", None, "remove the old shot")
        else:
            set_id, _ = r.write("POST", "/v1/appScreenshotSets", {"data": {"type": "appScreenshotSets", "attributes": {"screenshotDisplayType": dtype},
                                                                            "relationships": {"appStoreVersionLocalization": rel("appStoreVersionLocalizations", loc)}}}, f"{size} set")
        for p in paths:
            upload_shot(r, set_id, p)

    out("")
    for line in click_list(privacy_md):
        out(line)
    out("No submission was made: submitting for review is a separate, explicit action.")
    return r


def upload_shot(r, set_id, path):
    size = os.path.getsize(path)
    with open(path, "rb") as f:
        data = f.read()
    sid, doc = r.write("POST", "/v1/appScreenshots", {"data": {"type": "appScreenshots", "attributes": {"fileName": os.path.basename(path), "fileSize": size},
                                                             "relationships": {"appScreenshotSet": rel("appScreenshotSets", set_id)}}}, os.path.basename(path))
    if r.dry:
        return
    for op in doc["data"]["attributes"]["uploadOperations"]:
        chunk = data[op["offset"]:op["offset"] + op["length"]]
        r.api.put_bytes(op["url"], [(h["name"], h["value"]) for h in op["requestHeaders"]], chunk)
    r.write("PATCH", f"/v1/appScreenshots/{sid}", {"data": {"type": "appScreenshots", "id": sid,
                                                          "attributes": {"uploaded": True, "sourceFileChecksum": hashlib.md5(data).hexdigest()}}}, "commit the upload")


def main(argv):
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", choices=["plan", "apply"])
    ap.add_argument("--version", default="1.4.3")
    ap.add_argument("--shots-dir", required=True, help="folder with <size>/<name>-dark.png from a store-shots run")
    ap.add_argument("--listing", default=os.path.join(HERE, "listing.md"))
    ap.add_argument("--privacy", default=os.path.join(HERE, "privacy-label.md"))
    a = ap.parse_args(argv)
    if not re.fullmatch(r"\d+\.\d+\.\d+", a.version):
        raise SystemExit("version must look like 1.4.3")
    for n in ("ASC_KEY_P8", "ASC_KEY_ID", "ASC_ISSUER_ID"):
        if not os.environ.get(n):
            raise SystemExit(f"missing secret {n}")
    api = Api(os.environ["ASC_KEY_P8"], os.environ["ASC_KEY_ID"], os.environ["ASC_ISSUER_ID"])
    with open(a.listing, encoding="utf-8") as f, open(a.privacy, encoding="utf-8") as g:
        run(api, a.mode, f.read(), g.read(), a.version, a.shots_dir, os.environ)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
