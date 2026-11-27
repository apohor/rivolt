#!/usr/bin/env python3
"""Plans the Rivolt Pro subscriptions in App Store Connect from ios/store/subscriptions.md (GitOps, like asc_listing.py).

  asc_subscriptions.py plan|apply --spec openapi.oas.json [--review-shot PATH] [--subscriptions PATH]

Plan reads from App Store Connect and prints every write it would make; it writes nothing. Apply makes those writes, after the same
checks pass, and only from the ios-store-subscriptions workflow (the `testflight` environment the owner approves).
It creates the subscription group (name, en-US display name), the monthly and yearly products (period, level, review note,
en-US name and description, United States availability), their prices at the exact $3.99 and $34.99 price points, the review
screenshot, and sets the App Store Server Notification URLs (production and sandbox, version 2) on the app.

The price point is looked up by customer price and the run stops if there is not exactly one match, or if an existing price
differs (a price change is a decision, not a script step). A product that does not exist yet has no price points to read, so
a plan cannot check its price: it says so, and the apply looks the price up right after creating the product and stops
there if there is no exact point (re-running continues; nothing is repeated).
Secrets come from the environment and are never printed: ASC_KEY_P8, ASC_KEY_ID, ASC_ISSUER_ID.
Every call is checked against Apple's OpenAPI spec first. Not seen: a run against the live API.
"""
import argparse
import hashlib
import importlib.util
import os
import re
import sys
from urllib.parse import quote

HERE = os.path.dirname(os.path.abspath(__file__))


def _load(name):
    spec = importlib.util.spec_from_file_location(name, os.path.join(HERE, name + ".py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


al = _load("asc_listing")
BUNDLE_ID = al.BUNDLE_ID
LOCALE = al.LOCALE
rel = al.rel
NEW = al.NEW
TERRITORY = "USA"
PERIODS = {"ONE_MONTH", "ONE_YEAR"}
LIMITS = {"name": 30, "description": 45, "group": 30, "note": 4000}
USED_OPS = [
    ("GET", "/v1/apps"), ("PATCH", "/v1/apps/{id}"),
    ("GET", "/v1/apps/{id}/subscriptionGroups"), ("POST", "/v1/subscriptionGroups"),
    ("GET", "/v1/subscriptionGroups/{id}/subscriptionGroupLocalizations"), ("POST", "/v1/subscriptionGroupLocalizations"),
    ("PATCH", "/v1/subscriptionGroupLocalizations/{id}"),
    ("GET", "/v1/subscriptionGroups/{id}/subscriptions"), ("POST", "/v1/subscriptions"), ("PATCH", "/v1/subscriptions/{id}"),
    ("GET", "/v1/subscriptions/{id}/subscriptionLocalizations"), ("POST", "/v1/subscriptionLocalizations"),
    ("PATCH", "/v1/subscriptionLocalizations/{id}"),
    ("GET", "/v1/subscriptions/{id}/planAvailabilities"), ("GET", "/v1/subscriptionPlanAvailabilities/{id}/availableTerritories"),
    ("POST", "/v1/subscriptionPlanAvailabilities"),
    ("GET", "/v1/subscriptions/{id}/pricePoints"), ("GET", "/v1/subscriptions/{id}/prices"), ("POST", "/v1/subscriptionPrices"),
    ("GET", "/v1/subscriptions/{id}/appStoreReviewScreenshot"), ("POST", "/v1/subscriptionAppStoreReviewScreenshots"),
    ("PATCH", "/v1/subscriptionAppStoreReviewScreenshots/{id}"), ("GET", "/v1/subscriptionAppStoreReviewScreenshots/{id}"),
    ("DELETE", "/v1/subscriptionAppStoreReviewScreenshots/{id}"),
]


def parse(md):
    """{section: {key: value}} from `## Section` headings and `Key: value` lines."""
    out, cur = {}, None
    for line in md.splitlines():
        if line.startswith("## "):
            cur = out.setdefault(line[3:].strip(), {})
        elif cur is not None and re.match(r"^[A-Za-z][A-Za-z ]*: ", line):
            k, v = line.split(": ", 1)
            cur[k.strip()] = v.strip()
    return out


def check(d):
    """Problems with the desired state, before anything is read or written."""
    p = []
    need = {"Group": ["Reference name", "Display name"], "Notifications": ["Version", "Production URL", "Sandbox URL"],
            "Review": ["Notes"]}
    for sec in ("Yearly", "Monthly"):
        need[sec] = ["Product ID", "Reference name", "Period", "Level", "Price", "Display name", "Description"]
    for sec, keys in need.items():
        for k in keys:
            if not d.get(sec, {}).get(k):
                p.append(f"{sec}: {k} is missing")
    if p:
        return p
    if len(d["Group"]["Display name"]) > LIMITS["group"]:
        p.append("Group: display name is over 30 characters")
    if len(d["Review"]["Notes"]) > LIMITS["note"]:
        p.append("Review: notes are over 4000 characters")
    if d["Notifications"]["Version"] not in ("V1", "V2"):
        p.append("Notifications: Version must be V1 or V2")
    for k in ("Production URL", "Sandbox URL"):
        if not d["Notifications"][k].startswith("https://"):
            p.append(f"Notifications: {k} must be https")
    ids = set()
    for sec in ("Yearly", "Monthly"):
        s = d[sec]
        if s["Period"] not in PERIODS:
            p.append(f"{sec}: Period must be one of {sorted(PERIODS)}")
        if not re.fullmatch(r"\d+\.\d\d", s["Price"]):
            p.append(f"{sec}: Price must look like 3.99")
        if not s["Level"].isdigit() or int(s["Level"]) < 1:
            p.append(f"{sec}: Level must be a positive number")
        if not re.fullmatch(r"dev\.rivolt\.app\.[a-z.]+", s["Product ID"]):
            p.append(f"{sec}: Product ID must start with dev.rivolt.app.")
        if len(s["Display name"]) > LIMITS["name"]:
            p.append(f"{sec}: display name is over 30 characters")
        if len(s["Description"]) > LIMITS["description"]:
            p.append(f"{sec}: description is over 45 characters")
        ids.add(s["Product ID"])
    if len(ids) != 2:
        p.append("Yearly and Monthly need different product ids")
    return p


# ---------- payload builders (pure) ----------

def group_payload(app, d):
    return {"data": {"type": "subscriptionGroups", "attributes": {"referenceName": d["Group"]["Reference name"]},
                     "relationships": {"app": rel("apps", app)}}}


def group_loc_attrs(d):
    return {"name": d["Group"]["Display name"]}


def product_attrs(s, note):
    return {"name": s["Reference name"], "subscriptionPeriod": s["Period"], "groupLevel": int(s["Level"]),
            "familySharable": False, "reviewNote": note}


def product_payload(group, s, note):
    return {"data": {"type": "subscriptions", "attributes": dict(product_attrs(s, note), productId=s["Product ID"]),
                     "relationships": {"group": rel("subscriptionGroups", group)}}}


def product_loc_attrs(s):
    return {"name": s["Display name"], "description": s["Description"]}


def availability_payload(sub):
    return {"data": {"type": "subscriptionPlanAvailabilities", "attributes": {"availableInNewTerritories": False, "planType": "UPFRONT"},
                     "relationships": {"subscription": rel("subscriptions", sub),
                                       "availableTerritories": {"data": [{"type": "territories", "id": TERRITORY}]}}}}


def price_payload(sub, point):
    return {"data": {"type": "subscriptionPrices", "attributes": {"preserveCurrentPrice": False},
                     "relationships": {"subscription": rel("subscriptions", sub), "territory": rel("territories", TERRITORY),
                                       "subscriptionPricePoint": rel("subscriptionPricePoints", point)}}}


def app_urls_attrs(n):
    return {"subscriptionStatusUrl": n["Production URL"], "subscriptionStatusUrlVersion": n["Version"],
            "subscriptionStatusUrlForSandbox": n["Sandbox URL"], "subscriptionStatusUrlVersionForSandbox": n["Version"]}


def exact_point(points, price):
    """The one price point whose customer price is exactly `price`; stops on none or several."""
    hit = [p["id"] for p in points if (p.get("attributes") or {}).get("customerPrice") == price]
    if len(hit) != 1:
        raise SystemExit(f"expected exactly one {TERRITORY} price point at ${price}, found {len(hit)}: stopping, nothing was priced")
    return hit[0]


# ---------- the run ----------

def _items(doc):
    return (doc or {}).get("data") or []


def lookups(r, d, shot, out):
    docs = r.get(f"/v1/apps?filter[bundleId]={BUNDLE_ID}")["data"]
    if len(docs) != 1:
        raise SystemExit(f"expected one app for {BUNDLE_ID}, found {len(docs)}")
    L = {"app": docs[0]["id"], "app_attrs": docs[0].get("attributes") or {}, "shot": None}
    if shot:
        with open(shot, "rb") as f:
            data = f.read()
        w, h = al.png_size(data)
        if min(w, h) < 640:
            raise SystemExit(f"the review screenshot is {w}x{h}: too small for App Store Connect")
        L["shot"] = data
        out(f"  review screenshot {os.path.basename(shot)} {w}x{h} sha256 {hashlib.sha256(data).hexdigest()}")
    else:
        out("  no --review-shot given: the plan lists the screenshot as still to do; an apply needs it")
    groups = r.get(f"/v1/apps/{L['app']}/subscriptionGroups?filter[referenceName]={quote(d['Group']['Reference name'])}")["data"]
    if len(groups) > 1:
        raise SystemExit(f"{len(groups)} subscription groups are named {d['Group']['Reference name']}: stopping")
    L["group"] = groups[0]["id"] if groups else None
    L["group_loc"] = None
    L["subs"] = {}
    if L["group"]:
        locs = r.get(f"/v1/subscriptionGroups/{L['group']}/subscriptionGroupLocalizations")["data"]
        L["group_loc"] = next(({"id": x["id"], "name": x["attributes"].get("name")} for x in locs if x["attributes"].get("locale") == LOCALE), None)
        for x in r.get(f"/v1/subscriptionGroups/{L['group']}/subscriptions?limit=200")["data"]:
            L["subs"][x["attributes"]["productId"]] = x
    return L


def sub_state(r, sub_id, s):
    """What an existing product already has: localization, availability territories, USA price, review screenshot."""
    st = {}
    locs = r.get(f"/v1/subscriptions/{sub_id}/subscriptionLocalizations")["data"]
    st["loc"] = next(({"id": x["id"], "attrs": x["attributes"]} for x in locs if x["attributes"].get("locale") == LOCALE), None)
    avail = r.get(f"/v1/subscriptions/{sub_id}/planAvailabilities")["data"]
    st["avail"] = None
    for a in avail:
        if (a["attributes"] or {}).get("planType") == "UPFRONT":
            terr = r.get(f"/v1/subscriptionPlanAvailabilities/{a['id']}/availableTerritories?limit=200")["data"]
            st["avail"] = sorted(t["id"] for t in terr)
    points = r.get(f"/v1/subscriptions/{sub_id}/pricePoints?filter[territory]={TERRITORY}&limit=8000")
    if (points.get("links") or {}).get("next"):
        raise SystemExit("the price point list has more than one page: stopping")
    st["point"] = exact_point(points["data"], s["Price"])
    prices = r.get(f"/v1/subscriptions/{sub_id}/prices?filter[territory]={TERRITORY}&include=subscriptionPricePoint&limit=50")
    cur = {i["id"]: (i.get("attributes") or {}).get("customerPrice") for i in prices.get("included", []) if i["type"] == "subscriptionPricePoints"}
    st["prices"] = sorted(set(cur.values()))
    shot = r.get(f"/v1/subscriptions/{sub_id}/appStoreReviewScreenshot", optional=True)
    node = (shot or {}).get("data")
    st["shot"] = {"id": node["id"], "md5": (node.get("attributes") or {}).get("sourceFileChecksum")} if node else None
    return st


def upload_review_shot(r, sub, data):
    sid, doc = r.write("POST", "/v1/subscriptionAppStoreReviewScreenshots",
                       {"data": {"type": "subscriptionAppStoreReviewScreenshots", "attributes": {"fileName": "review.png", "fileSize": len(data)},
                                 "relationships": {"subscription": rel("subscriptions", sub)}}}, "review screenshot")
    if r.dry:
        return
    for op in doc["data"]["attributes"]["uploadOperations"]:
        status = r.api.put_bytes(op["url"], [(h["name"], h["value"]) for h in op["requestHeaders"]], data[op["offset"]:op["offset"] + op["length"]])
        if status >= 300:
            raise SystemExit(f"uploading the review screenshot failed with {status}")
    r.write("PATCH", f"/v1/subscriptionAppStoreReviewScreenshots/{sid}",
            {"data": {"type": "subscriptionAppStoreReviewScreenshots", "id": sid,
                      "attributes": {"uploaded": True, "sourceFileChecksum": hashlib.md5(data).hexdigest()}}}, "commit the upload")


def run_pass(api, dry, d, shot, out, spec):
    r = al.Run(api, dry, out, spec)
    L = lookups(r, d, shot, out)
    app = L["app"]

    out("app: App Store Server Notification URLs")
    want = app_urls_attrs(d["Notifications"])
    if all(L["app_attrs"].get(k) == v for k, v in want.items()):
        out("  already set: skipped")
    else:
        r.write("PATCH", f"/v1/apps/{app}", {"data": {"type": "apps", "id": app, "attributes": want}})

    out("subscription group")
    group = L["group"]
    if not group:
        group, _ = r.write("POST", "/v1/subscriptionGroups", group_payload(app, d), "create the group")
    else:
        out("  the group exists: skipped")
    gl = L["group_loc"]
    if gl and gl["name"] == d["Group"]["Display name"]:
        out("  display name already set: skipped")
    elif gl:
        r.write("PATCH", f"/v1/subscriptionGroupLocalizations/{gl['id']}", {"data": {"type": "subscriptionGroupLocalizations", "id": gl["id"], "attributes": group_loc_attrs(d)}})
    else:
        r.write("POST", "/v1/subscriptionGroupLocalizations", {"data": {"type": "subscriptionGroupLocalizations", "attributes": dict(group_loc_attrs(d), locale=LOCALE),
                                                                         "relationships": {"subscriptionGroup": rel("subscriptionGroups", group)}}})

    note = d["Review"]["Notes"]
    for sec in ("Yearly", "Monthly"):
        s = d[sec]
        out(f"{sec.lower()} product {s['Product ID']}")
        cur = L["subs"].get(s["Product ID"])
        if cur:
            sub = cur["id"]
            if cur["attributes"].get("subscriptionPeriod") != s["Period"]:
                raise SystemExit(f"{s['Product ID']} exists with period {cur['attributes'].get('subscriptionPeriod')}, not {s['Period']}: a period cannot change; stopping")
            want_attrs = product_attrs(s, note)
            diff = {k: v for k, v in want_attrs.items() if cur["attributes"].get(k) != v and k != "subscriptionPeriod"}
            if diff:
                r.write("PATCH", f"/v1/subscriptions/{sub}", {"data": {"type": "subscriptions", "id": sub, "attributes": diff}}, "update " + ", ".join(sorted(diff)))
            else:
                out("  product attributes already set: skipped")
            st = sub_state(r, sub, s)
        else:
            sub, _ = r.write("POST", "/v1/subscriptions", product_payload(group, s, note), "create the product")
            st = {"loc": None, "avail": None, "point": None, "prices": [], "shot": None}
        # localization
        if st["loc"]:
            if all(st["loc"]["attrs"].get(k) == v for k, v in product_loc_attrs(s).items()):
                out("  en-US name and description already set: skipped")
            else:
                r.write("PATCH", f"/v1/subscriptionLocalizations/{st['loc']['id']}", {"data": {"type": "subscriptionLocalizations", "id": st["loc"]["id"], "attributes": product_loc_attrs(s)}})
        else:
            r.write("POST", "/v1/subscriptionLocalizations", {"data": {"type": "subscriptionLocalizations", "attributes": dict(product_loc_attrs(s), locale=LOCALE),
                                                                      "relationships": {"subscription": rel("subscriptions", sub)}}})
        # availability
        if st["avail"] == [TERRITORY]:
            out("  available in the United States only: skipped")
        elif st["avail"]:
            raise SystemExit(f"{s['Product ID']} is available in {', '.join(st['avail'])}, not {TERRITORY} only: stopping")
        else:
            r.write("POST", "/v1/subscriptionPlanAvailabilities", availability_payload(sub), "United States only")
        # price
        if st["prices"] and st["prices"] != [s["Price"]]:
            raise SystemExit(f"{s['Product ID']} is priced {', '.join(st['prices'])} in {TERRITORY}, not {s['Price']}: a price change is not made by this script; stopping")
        if st["prices"]:
            out(f"  price already ${s['Price']}: skipped")
        else:
            point = st["point"]
            if point is None and not r.dry:
                pts = r.get(f"/v1/subscriptions/{sub}/pricePoints?filter[territory]={TERRITORY}&limit=8000")
                point = exact_point(pts["data"], s["Price"])
            if point is None:
                out(f"  WOULD price it ${s['Price']} a {s['Period'].split('_')[1].lower()} in {TERRITORY}: the price point can be looked up only once the product exists (the apply stops there if there is no exact ${s['Price']} point)")
            else:
                r.write("POST", "/v1/subscriptionPrices", price_payload(sub, point), f"${s['Price']}")
        # review screenshot
        if L["shot"] is None:
            out("  review screenshot: not given, still to do")
        elif st["shot"] and st["shot"]["md5"] == hashlib.md5(L["shot"]).hexdigest():
            out("  review screenshot already uploaded: skipped")
        else:
            if st["shot"]:
                r.write("DELETE", f"/v1/subscriptionAppStoreReviewScreenshots/{st['shot']['id']}", None, "replace the review screenshot")
            upload_review_shot(r, sub, L["shot"])
    out("")
    out("No submission was made: attaching the products to a version and submitting stay separate, explicit actions.")
    return r


def run(api, mode, md, shot, out=print, spec=None):
    d = parse(md)
    problems = check(d)
    if problems:
        raise SystemExit("subscriptions.md fails its checks: " + "; ".join(problems))
    if spec is not None:
        out(f"every call is checked against the App Store Connect OpenAPI spec {_load('asc_spec').version(spec)}")
    out(f"{'PLAN (nothing is written)' if mode != 'apply' else 'APPLY'} for {BUNDLE_ID}")
    if mode == "apply":
        run_pass(api, True, d, shot, lambda *_: None, spec)
        out("checks passed; writing")
    return run_pass(api, mode != "apply", d, shot, out, spec)


def main(argv):
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", choices=["plan", "apply"], help="plan writes nothing; apply writes")
    ap.add_argument("--spec", required=True)
    ap.add_argument("--review-shot", default=None, help="PNG of the purchase screen for the App Review team")
    ap.add_argument("--subscriptions", default=os.path.join(HERE, "subscriptions.md"))
    a = ap.parse_args(argv)
    for n in ("ASC_KEY_P8", "ASC_KEY_ID", "ASC_ISSUER_ID"):
        if not os.environ.get(n):
            raise SystemExit(f"missing secret {n}")
    api = al.Api(os.environ["ASC_KEY_P8"], os.environ["ASC_KEY_ID"], os.environ["ASC_ISSUER_ID"])
    with open(a.subscriptions, encoding="utf-8") as f:
        run(api, a.mode, f.read(), a.review_shot, spec=_load("asc_spec").load(a.spec))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
