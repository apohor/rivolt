#!/usr/bin/env python3
"""Asks Apple to send a TEST App Store Server Notification to the Sandbox URL set in App Store Connect, then reads back how it went.

POST /inApps/v1/notifications/test on the Sandbox host, then GET /inApps/v1/notifications/test/{token} (Apple's own record of the send
attempts and what our server answered). Sends nothing to users, changes no account: the server ignores TEST. Prints only the status and the
send-attempt result, never the key or the token. Run by hand from the ios-store-test-notification workflow (testflight environment).
Not seen: whether the App Store Connect team key is accepted by the Server API (Apple documents keys from the In-App Purchase tab);
a 401 below says so.
"""
import importlib.util
import json
import os
import sys
import time

HOST = "https://api.storekit-sandbox.itunes.apple.com"


def _al():
    here = os.path.dirname(os.path.abspath(__file__))
    spec = importlib.util.spec_from_file_location("asc_listing", os.path.join(here, "asc_listing.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


class ServerApi:
    def __init__(self, api):
        self.api = api

    def call(self, method, path):
        import urllib.request
        import urllib.error
        req = urllib.request.Request(HOST + path, method=method, headers={"Authorization": "Bearer " + self.api._token()})
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


def verdict(status_doc):
    """One line from the test-notification record: the first send attempt's result as Apple logged it."""
    attempts = (status_doc or {}).get("sendAttempts") or []
    first = (status_doc or {}).get("firstSendAttemptResult") or (attempts[0].get("sendAttemptResult") if attempts else "")
    return first or "NO_ATTEMPT_YET"


def main():
    al = _al()
    key = os.environ["ASC_KEY_P8"]
    api = ServerApi(al.Api(key, os.environ["ASC_KEY_ID"], os.environ["ASC_ISSUER_ID"], extra_claims={"bid": al.BUNDLE_ID}))
    code, doc = api.call("POST", "/inApps/v1/notifications/test")
    print(f"request test notification: HTTP {code}")
    if code != 200 or "testNotificationToken" not in doc:
        if code == 401:
            print("401: Apple refused this key for the App Store Server API; it needs an In-App Purchase key (App Store Connect > Users and Access > Integrations > In-App Purchase).")
        print("error:", doc.get("errorCode"), doc.get("errorMessage"))
        return 1
    token = doc["testNotificationToken"]
    result = "NO_ATTEMPT_YET"
    for _ in range(6):
        time.sleep(10)
        code, st = api.call("GET", f"/inApps/v1/notifications/test/{token}")
        print(f"status check: HTTP {code}")
        if code == 200:
            result = verdict(st)
            print("first send attempt result:", result)
            break
    return 0 if result == "SUCCESS" else 1


if __name__ == "__main__":
    sys.exit(main())
