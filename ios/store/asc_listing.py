#!/usr/bin/env python3
"""Applies ios/store/listing.md and ios/store/privacy-label.md to App Store Connect through its API (GitOps for the listing).

  asc_listing.py plan|apply --spec openapi.oas.json --shots-dir DIR [--version 1.4.3] [--listing PATH]

`plan` (the workflow's default) reads from App Store Connect and prints every write it would make; it writes nothing.
`apply` makes those writes. Neither submits the version for review: submission stays a separate, explicit action.
App Privacy has no public API, so both modes print the click list for the owner from privacy-label.md.

Secrets come from the environment and are never printed: ASC_KEY_P8 (key text), ASC_KEY_ID, ASC_ISSUER_ID, and for the
review details ASC_REVIEW_CONTACT_FIRST_NAME, ASC_REVIEW_CONTACT_LAST_NAME, ASC_REVIEW_CONTACT_PHONE,
ASC_REVIEW_CONTACT_EMAIL, and optionally ASC_REVIEW_USER with ASC_REVIEW_PASSWORD (without them the reviewer uses the demo
truck and no credentials are sent). The account, password and contact are masked in the plan.

Standard library plus the openssl command-line tool. Every request, query and body is checked against Apple's published
OpenAPI spec (asc_spec.py) before it is sent, in plan and in apply (apply runs a full dry pass first). Not seen: a run
against the live API. The pure builders and the run are unit-tested with a fake client against a vendored subset of the
spec (test_asc_listing.py, asc-openapi-subset.json).
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
# Size folder -> App Store Connect display type. Apple's spec 4.6.1 has no 6.9" or 6.3" type: APP_IPHONE_67 is the set
# that takes the 6.9" 1320x2868 shots, and the 6.9" set is the one Apple requires. The 6.3" shots are not uploaded.
DISPLAY_TYPES = {"6.9": "APP_IPHONE_67"}
SHOT_PIXELS = {"6.9": (1320, 2868)}
MAX_SHOTS = 10  # App Store Connect holds at most ten screenshots in a set
EDITABLE = {"PREPARE_FOR_SUBMISSION", "DEVELOPER_REJECTED", "REJECTED", "METADATA_REJECTED", "WAITING_FOR_REVIEW", "IN_REVIEW",
            "READY_FOR_REVIEW", "INVALID_BINARY", "WAITING_FOR_EXPORT_COMPLIANCE"}
# Every answer of Apple's age-rating questionnaire (spec 4.6.1): no content, no social, ads or purchases features.
AGE_NONE = {
    "alcoholTobaccoOrDrugUseOrReferences": "NONE", "contests": "NONE", "gamblingSimulated": "NONE", "gunsOrOtherWeapons": "NONE",
    "horrorOrFearThemes": "NONE", "matureOrSuggestiveThemes": "NONE", "medicalOrTreatmentInformation": "NONE",
    "profanityOrCrudeHumor": "NONE", "sexualContentGraphicAndNudity": "NONE", "sexualContentOrNudity": "NONE",
    "violenceCartoonOrFantasy": "NONE", "violenceRealistic": "NONE", "violenceRealisticProlongedGraphicOrSadistic": "NONE",
    "advertising": False, "ageAssurance": False, "datingAndRelationships": False, "gambling": False,
    "healthOrWellnessTopics": False, "lootBox": False, "messagingAndChat": False, "parentalControls": False,
    "sharedLocation": False, "socialMedia": False, "unrestrictedWebAccess": False, "userGeneratedContent": False,
}
# Every (method, path template) this script calls: the spec check and the test subset cover exactly these.
USED_OPS = [
    ("GET", "/v1/apps"), ("PATCH", "/v1/apps/{id}"), ("GET", "/v1/builds"), ("PATCH", "/v1/builds/{id}"),
    ("GET", "/v1/apps/{id}/appAvailabilityV2"), ("GET", "/v2/appAvailabilities/{id}/territoryAvailabilities"),
    ("POST", "/v2/appAvailabilities"), ("GET", "/v1/apps/{id}/appPricePoints"),
    ("GET", "/v1/apps/{id}/appPriceSchedule"), ("GET", "/v1/appPriceSchedules/{id}/manualPrices"),
    ("POST", "/v1/appPriceSchedules"), ("GET", "/v1/apps/{id}/appStoreVersions"), ("POST", "/v1/appStoreVersions"),
    ("PATCH", "/v1/appStoreVersions/{id}"), ("GET", "/v1/appStoreVersions/{id}/appStoreVersionLocalizations"),
    ("POST", "/v1/appStoreVersionLocalizations"), ("PATCH", "/v1/appStoreVersionLocalizations/{id}"),
    ("GET", "/v1/apps/{id}/appInfos"), ("PATCH", "/v1/appInfos/{id}"), ("GET", "/v1/appInfos/{id}/appInfoLocalizations"),
    ("POST", "/v1/appInfoLocalizations"), ("PATCH", "/v1/appInfoLocalizations/{id}"),
    ("GET", "/v1/appInfos/{id}/ageRatingDeclaration"), ("PATCH", "/v1/ageRatingDeclarations/{id}"),
    ("GET", "/v1/appStoreVersions/{id}/appStoreReviewDetail"), ("POST", "/v1/appStoreReviewDetails"),
    ("PATCH", "/v1/appStoreReviewDetails/{id}"), ("GET", "/v1/appStoreVersionLocalizations/{id}/appScreenshotSets"),
    ("POST", "/v1/appScreenshotSets"), ("GET", "/v1/appScreenshotSets/{id}/appScreenshots"),
    ("POST", "/v1/appScreenshots"), ("PATCH", "/v1/appScreenshots/{id}"), ("GET", "/v1/appScreenshots/{id}"),
    ("DELETE", "/v1/appScreenshots/{id}"),
]
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
    """Contact from secrets (all four required). A review sign-in account is optional: with none, the reviewer uses the demo
    truck on the sign-in screen, and no credentials are sent."""
    need = ["ASC_REVIEW_CONTACT_FIRST_NAME", "ASC_REVIEW_CONTACT_LAST_NAME", "ASC_REVIEW_CONTACT_PHONE", "ASC_REVIEW_CONTACT_EMAIL"]
    missing = [n for n in need if not env.get(n)]
    if missing:
        raise SystemExit("missing secrets (by name): " + ", ".join(missing))
    user, password = env.get("ASC_REVIEW_USER"), env.get("ASC_REVIEW_PASSWORD")
    if bool(user) != bool(password):
        raise SystemExit("ASC_REVIEW_USER and ASC_REVIEW_PASSWORD must be set together or both left out")
    a = {"contactFirstName": env["ASC_REVIEW_CONTACT_FIRST_NAME"], "contactLastName": env["ASC_REVIEW_CONTACT_LAST_NAME"],
         "contactPhone": env["ASC_REVIEW_CONTACT_PHONE"], "contactEmail": env["ASC_REVIEW_CONTACT_EMAIL"],
         "demoAccountRequired": bool(user), "notes": s["Review notes"]}
    if user:
        a.update(demoAccountName=user, demoAccountPassword=password)
    return a


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
        try:
            with urllib.request.urlopen(req, timeout=120) as r:
                return r.status
        except urllib.error.HTTPError as e:
            return e.code


def errors_of(doc):
    return "; ".join(f"{e.get('code', '')} {e.get('title', '')}: {e.get('detail', '')}" for e in (doc or {}).get("errors", []))[:400]


# ---------- the run ----------

NEW = "(new)"


class Run:
    """One pass over the listing. dry: reads are real, writes are only printed. spec: every call is checked against it first."""

    def __init__(self, api, dry, out=print, spec=None):
        self.api, self.dry, self.out, self.spec = api, dry, out, spec
        self.writes = []

    def _spec_check(self, method, path, body=None):
        if self.spec is not None:
            problems = _load("asc_spec").validate_call(self.spec, method, path, body)
            if problems:
                raise SystemExit(f"{method} {path} does not match the App Store Connect spec: " + "; ".join(problems[:6]))

    def get(self, path, optional=False):
        self._spec_check("GET", path)
        status, doc = self.api.request("GET", path)
        if optional and status == 404:
            return None
        if status != 200:
            raise SystemExit(f"GET {path} -> {status} {errors_of(doc)}")
        return doc

    def write(self, method, path, body=None, note=""):
        self._spec_check(method, path, body)
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


def png_size(data):
    if data[:8] != b"\x89PNG\r\n\x1a\n" or data[12:16] != b"IHDR":
        raise SystemExit("a store shot is not a PNG")
    return int.from_bytes(data[16:20], "big"), int.from_bytes(data[20:24], "big")


def read_shots(shots_dir, out):
    """Reads every shot, checks its pixel size, and prints size and sha256 so the approver can compare with the images."""
    files = screenshot_files(shots_dir)
    shots = {}
    for size, paths in files.items():
        shots[size] = []
        for p in paths:
            with open(p, "rb") as f:
                data = f.read()
            w, h = png_size(data)
            if (w, h) != SHOT_PIXELS[size]:
                raise SystemExit(f"{os.path.relpath(p, shots_dir)} is {w}x{h}; {size} shots must be {SHOT_PIXELS[size][0]}x{SHOT_PIXELS[size][1]}")
            digest = hashlib.sha256(data).hexdigest()
            out(f"  shot {size}/{os.path.basename(p)} {w}x{h} sha256 {digest}")
            shots[size].append((p, data))
    return shots


def lookups(r, s, version, env, shots_dir, out):
    """Every check and read, before the first write of the pass. Returns what the writes need."""
    docs = r.get(f"/v1/apps?filter[bundleId]={BUNDLE_ID}")["data"]
    if len(docs) != 1:
        raise SystemExit(f"expected one app for {BUNDLE_ID}, found {len(docs)}")
    app = docs[0]["id"]
    builds = r.get(f"/v1/builds?filter[app]={app}&filter[preReleaseVersion.version]={version}&sort=-uploadedDate&limit=1")["data"]
    if not builds or builds[0]["attributes"].get("processingState") != "VALID":
        raise SystemExit(f"no processed (VALID) build for {version}: upload it first")
    L = {"app": app, "build": builds[0]["id"]}
    L["review"] = review_detail_attrs(s, env)
    out("store shots")
    L["shots"] = read_shots(shots_dir, out)

    points = r.get(f"/v1/apps/{app}/appPricePoints?filter[territory]=USA&limit=200")["data"]
    free = [p["id"] for p in points if p.get("attributes", {}).get("customerPrice") in ("0", "0.0", "0.00")]
    if len(free) != 1:
        raise SystemExit(f"expected one free USA price point, found {len(free)}")
    L["free_point"] = free[0]

    avail = r.get(f"/v1/apps/{app}/appAvailabilityV2", optional=True)
    L["usa_only"] = False
    if avail and avail.get("data"):
        terrs = r.get(f"/v2/appAvailabilities/{avail['data']['id']}/territoryAvailabilities?include=territory&limit=200")["data"]
        on = {(t.get("relationships", {}).get("territory", {}).get("data") or {}).get("id") for t in terrs if t["attributes"].get("available")}
        L["usa_only"] = on == {"USA"}
    sched = r.get(f"/v1/apps/{app}/appPriceSchedule", optional=True)
    L["free_price"] = False
    if sched and sched.get("data"):
        prices = r.get(f"/v1/appPriceSchedules/{sched['data']['id']}/manualPrices?include=appPricePoint&limit=50")
        pts = {i["id"]: i["attributes"].get("customerPrice") for i in prices.get("included", []) if i["type"] == "appPricePoints"}
        L["free_price"] = bool(pts) and all(v in ("0", "0.0", "0.00") for v in pts.values())

    versions = r.get(f"/v1/apps/{app}/appStoreVersions?filter[platform]=IOS&limit=200")["data"]
    states = [(v, v["attributes"].get("appVersionState") or v["attributes"].get("appStoreState")) for v in versions]
    L["mine"] = next((v["id"] for v in versions if v["attributes"]["versionString"] == version), None)
    L["rename"] = None
    others = [(v, st) for v, st in states if v["attributes"]["versionString"] != version and st in EDITABLE]
    if others:
        # A never-released app has one untouched first version (1.0): use it and set its number, rather than stop.
        # Anything else (a released version, or two editable ones) is not this script's to guess at: stop.
        (v, st), = others[:1]
        if len(versions) == 1 and not L["mine"]:
            L["rename"] = v["id"]
            L["mine"] = v["id"]
            out(f"  the app has no released version and one editable version ({v['attributes']['versionString']}, {st}): it becomes {version}")
        else:
            raise SystemExit(f"another editable version exists ({v['attributes']['versionString']}, {st}): finish or remove it first")
    L["first"] = L["rename"] is not None or all(v["attributes"]["versionString"] == version for v in versions)

    infos = r.get(f"/v1/apps/{app}/appInfos")["data"]
    editable = [i for i in infos if (i["attributes"].get("state") or i["attributes"].get("appStoreState")) in EDITABLE] or infos
    L["info"] = editable[0]["id"]
    L["info_loc"] = next((x["id"] for x in r.get(f"/v1/appInfos/{L['info']}/appInfoLocalizations")["data"] if x["attributes"]["locale"] == LOCALE), None)
    L["age"] = r.get(f"/v1/appInfos/{L['info']}/ageRatingDeclaration")["data"]["id"]
    return L


def run_pass(api, dry, s, privacy_md, version, shots_dir, env, out, spec):
    r = Run(api, dry, out, spec)
    L = lookups(r, s, version, env, shots_dir, out)
    app, build = L["app"], L["build"]

    out("content rights, availability, price")
    r.write("PATCH", f"/v1/apps/{app}", {"data": {"type": "apps", "id": app, "attributes": {"contentRightsDeclaration": "USES_THIRD_PARTY_CONTENT"}}})
    if L["usa_only"]:
        out("  availability is already United States only: skipped")
    else:
        r.write("POST", "/v2/appAvailabilities", availability_payload(app), "United States only")
    if L["free_price"]:
        out("  price is already free: skipped")
    else:
        r.write("POST", "/v1/appPriceSchedules", price_schedule_payload(app, L["free_point"]), "free")

    out("version")
    ver = L["mine"]
    if ver:
        attrs = dict(version_attrs(), versionString=version) if L["rename"] else version_attrs()
        r.write("PATCH", f"/v1/appStoreVersions/{ver}", {"data": {"type": "appStoreVersions", "id": ver, "attributes": attrs}}, f"set the number to {version}" if L["rename"] else "")
    else:
        ver, _ = r.write("POST", "/v1/appStoreVersions", {"data": {"type": "appStoreVersions", "attributes": dict(version_attrs(), versionString=version, platform="IOS"),
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
    attrs = version_localization_attrs(s, L["first"])
    if L["first"]:
        out("  What's New is not sent: App Store Connect has none on a first version")
    if loc:
        r.write("PATCH", f"/v1/appStoreVersionLocalizations/{loc}", {"data": {"type": "appStoreVersionLocalizations", "id": loc, "attributes": attrs}})
    else:
        loc, _ = r.write("POST", "/v1/appStoreVersionLocalizations", {"data": {"type": "appStoreVersionLocalizations", "attributes": dict(attrs, locale=LOCALE),
                                                                                "relationships": {"appStoreVersion": rel("appStoreVersions", ver)}}})

    out("app info: subtitle, privacy URL, categories, age rating")
    info = L["info"]
    r.write("PATCH", f"/v1/appInfos/{info}", {"data": {"type": "appInfos", "id": info, "relationships": app_info_rel()}})
    if L["info_loc"]:
        r.write("PATCH", f"/v1/appInfoLocalizations/{L['info_loc']}", {"data": {"type": "appInfoLocalizations", "id": L["info_loc"], "attributes": app_info_localization_attrs(s)}})
    else:
        r.write("POST", "/v1/appInfoLocalizations", {"data": {"type": "appInfoLocalizations", "attributes": dict(app_info_localization_attrs(s), locale=LOCALE),
                                                              "relationships": {"appInfo": rel("appInfos", info)}}})
    r.write("PATCH", f"/v1/ageRatingDeclarations/{L['age']}", {"data": {"type": "ageRatingDeclarations", "id": L["age"], "attributes": AGE_NONE}}, "all none (4+)")

    out("review details")
    detail = None
    if ver != NEW:
        d = r.get(f"/v1/appStoreVersions/{ver}/appStoreReviewDetail", optional=True)
        detail = d["data"]["id"] if d and d.get("data") else None
    if detail:
        r.write("PATCH", f"/v1/appStoreReviewDetails/{detail}", {"data": {"type": "appStoreReviewDetails", "id": detail, "attributes": L["review"]}})
    else:
        r.write("POST", "/v1/appStoreReviewDetails", {"data": {"type": "appStoreReviewDetails", "attributes": L["review"],
                                                               "relationships": {"appStoreVersion": rel("appStoreVersions", ver)}}})

    out("screenshots (upload the new set, removing old shots only as the ten-shot cap needs; the rest after)")
    out(f"  uploading the {SHOT_MODE} set only; the {'light' if SHOT_MODE == 'dark' else 'dark'} shots are not uploaded (listing.md: {SHOT_MODE} is the primary set)")
    for size, shots in L["shots"].items():
        dtype = DISPLAY_TYPES[size]
        set_id, old = None, []
        if loc and loc != NEW:
            for st in r.get(f"/v1/appStoreVersionLocalizations/{loc}/appScreenshotSets")["data"]:
                if st["attributes"]["screenshotDisplayType"] == dtype:
                    set_id = st["id"]
            if set_id:
                old = [(x["id"], (x.get("attributes") or {}).get("sourceFileChecksum")) for x in r.get(f"/v1/appScreenshotSets/{set_id}/appScreenshots")["data"]]
        if old and [c for _, c in old] == [hashlib.md5(d).hexdigest() for _, d in shots]:
            out(f"  {size} set already holds exactly these {len(old)} shots: skipped")
            continue
        if not set_id:
            set_id, _ = r.write("POST", "/v1/appScreenshotSets", {"data": {"type": "appScreenshotSets", "attributes": {"screenshotDisplayType": dtype},
                                                                            "relationships": {"appStoreVersionLocalization": rel("appStoreVersionLocalizations", loc)}}}, f"{size} set")
        old_ids = [i for i, _ in old]
        done = 0
        for path, data in shots:
            # A set holds at most MAX_SHOTS: remove only as many old shots as the next upload needs.
            while old_ids and len(old_ids) + done + 1 > MAX_SHOTS:
                r.write("DELETE", f"/v1/appScreenshots/{old_ids.pop(0)}", None, "remove an old shot (the set is full)")
            try:
                upload_shot(r, set_id, path, data)
            except SystemExit as e:
                raise SystemExit(f"{e}. The {size} set now holds {len(old_ids)} old and {done} new shots (old ids: {', '.join(old_ids) or 'none'}); run again to finish") from None
            done += 1
        for sid in old_ids:
            r.write("DELETE", f"/v1/appScreenshots/{sid}", None, "remove an old shot (the new set is uploaded)")

    out("")
    for line in click_list(privacy_md):
        out(line)
    out("No submission was made: submitting for review is a separate, explicit action.")
    return r


def run(api, mode, listing_md, privacy_md, version, shots_dir, env, out=print, spec=None):
    s = _load("check_listing").sections(listing_md)
    problems = _load("check_listing").check(listing_md)
    if problems:
        raise SystemExit("listing.md fails its checks: " + "; ".join(problems))
    if spec is not None:
        out(f"every call is checked against the App Store Connect OpenAPI spec {_load('asc_spec').version(spec)}")
    out(f"{'PLAN (nothing is written)' if mode != 'apply' else 'APPLY'} for {BUNDLE_ID} version {version}")
    if mode == "apply":
        # A full dry pass first: every lookup, shot, secret and body is checked before the first real write.
        run_pass(api, True, s, privacy_md, version, shots_dir, env, lambda *_: None, spec)
        out("checks passed; writing")
    return run_pass(api, mode != "apply", s, privacy_md, version, shots_dir, env, out, spec)


def upload_shot(r, set_id, path, data):
    size = len(data)
    sid, doc = r.write("POST", "/v1/appScreenshots", {"data": {"type": "appScreenshots", "attributes": {"fileName": os.path.basename(path), "fileSize": size},
                                                             "relationships": {"appScreenshotSet": rel("appScreenshotSets", set_id)}}}, os.path.basename(path))
    if r.dry:
        return
    for op in doc["data"]["attributes"]["uploadOperations"]:
        chunk = data[op["offset"]:op["offset"] + op["length"]]
        status = r.api.put_bytes(op["url"], [(h["name"], h["value"]) for h in op["requestHeaders"]], chunk)
        if status >= 300:
            raise SystemExit(f"uploading {os.path.basename(path)} failed with {status}; the old shots are untouched")
    r.write("PATCH", f"/v1/appScreenshots/{sid}", {"data": {"type": "appScreenshots", "id": sid,
                                                          "attributes": {"uploaded": True, "sourceFileChecksum": hashlib.md5(data).hexdigest()}}}, "commit the upload")
    for _ in range(40):
        state = r.get(f"/v1/appScreenshots/{sid}")["data"]["attributes"].get("assetDeliveryState") or {}
        if state.get("state") == "COMPLETE":
            return
        if state.get("state") == "FAILED":
            raise SystemExit(f"Apple rejected {os.path.basename(path)}: {state.get('errors')}; the old shots are untouched")
        time.sleep(3)
    raise SystemExit(f"{os.path.basename(path)} did not finish processing; the old shots are untouched")


def main(argv):
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", choices=["plan", "apply"])
    ap.add_argument("--version", default="1.4.3")
    ap.add_argument("--shots-dir", required=True, help="folder with <size>/<name>-dark.png from a store-shots run")
    ap.add_argument("--listing", default=os.path.join(HERE, "listing.md"))
    ap.add_argument("--spec", required=True, help="Apple's App Store Connect OpenAPI JSON; every call is checked against it")
    ap.add_argument("--privacy", default=os.path.join(HERE, "privacy-label.md"))
    a = ap.parse_args(argv)
    if not re.fullmatch(r"\d+\.\d+\.\d+", a.version):
        raise SystemExit("version must look like 1.4.3")
    for n in ("ASC_KEY_P8", "ASC_KEY_ID", "ASC_ISSUER_ID"):
        if not os.environ.get(n):
            raise SystemExit(f"missing secret {n}")
    api = Api(os.environ["ASC_KEY_P8"], os.environ["ASC_KEY_ID"], os.environ["ASC_ISSUER_ID"])
    with open(a.listing, encoding="utf-8") as f, open(a.privacy, encoding="utf-8") as g:
        run(api, a.mode, f.read(), g.read(), a.version, a.shots_dir, os.environ, spec=_load('asc_spec').load(a.spec))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
