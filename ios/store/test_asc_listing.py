import importlib.util
import os
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("asc_listing", os.path.join(HERE, "asc_listing.py"))
al = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(al)
_cl = al._load("check_listing")

ENV = {"ASC_REVIEW_USER": "reviewer-user", "ASC_REVIEW_PASSWORD": "s3cret-pass", "ASC_REVIEW_CONTACT_FIRST_NAME": "Rae",
       "ASC_REVIEW_CONTACT_LAST_NAME": "Viewer", "ASC_REVIEW_CONTACT_PHONE": "+15550100", "ASC_REVIEW_CONTACT_EMAIL": "rv@example.invalid"}


def read(name):
    with open(os.path.join(HERE, name), encoding="utf-8") as f:
        return f.read()


class FakeApi:
    """Answers the GETs a run makes; records every write. versions: list of version strings already on the app."""

    def __init__(self, versions=(), build_state="VALID", loc=True):
        self.versions, self.build_state, self.loc, self.calls = list(versions), build_state, loc, []

    def put_bytes(self, url, headers, chunk):
        self.calls.append(("PUT", url, len(chunk)))
        return 200

    def request(self, method, path, body=None):
        self.calls.append((method, path, body))
        if method != "GET":
            if path == "/v1/appScreenshots" and method == "POST":
                return 201, {"data": {"id": "shot1", "attributes": {"uploadOperations": [
                    {"offset": 0, "length": 4, "url": "https://up.example.invalid/x", "requestHeaders": [{"name": "Content-Type", "value": "image/png"}]}]}}}
            return 200, {"data": {"id": "new-" + path.split("/")[-1], "attributes": {}}}
        if path.startswith("/v1/apps?filter"):
            return 200, {"data": [{"id": "APP"}]}
        if "/appPricePoints" in path:
            return 200, {"data": [{"id": "pp0", "attributes": {"customerPrice": "0.0"}}, {"id": "pp1", "attributes": {"customerPrice": "0.99"}}]}
        if path.endswith("/appStoreVersions?filter[platform]=IOS&limit=200"):
            return 200, {"data": [{"id": "V" + v, "attributes": {"versionString": v}} for v in self.versions]}
        if path.startswith("/v1/builds"):
            return 200, {"data": [{"id": "B1", "attributes": {"processingState": self.build_state}}]}
        if path.endswith("/appStoreVersionLocalizations"):
            return 200, {"data": [{"id": "L1", "attributes": {"locale": "en-US"}}] if self.loc else []}
        if path.endswith("/appInfos"):
            return 200, {"data": [{"id": "I1", "attributes": {"appStoreState": "PREPARE_FOR_SUBMISSION"}}]}
        if path.endswith("/appInfoLocalizations"):
            return 200, {"data": [{"id": "IL1", "attributes": {"locale": "en-US"}}]}
        if path.endswith("/ageRatingDeclaration"):
            return 200, {"data": {"id": "AG1"}}
        if path.endswith("/appStoreReviewDetail"):
            return 200, {"data": None}
        if path.endswith("/appScreenshotSets"):
            return 200, {"data": [{"id": "S69", "attributes": {"screenshotDisplayType": "APP_IPHONE_69"}}]}
        if path.endswith("/appScreenshots"):
            return 200, {"data": [{"id": "old1"}, {"id": "old2"}]}
        raise AssertionError("unexpected GET " + path)

    def writes(self):
        return [c for c in self.calls if c[0] in ("POST", "PATCH", "DELETE", "PUT")]


def shots_dir():
    d = tempfile.mkdtemp()
    for size in al.DISPLAY_TYPES:
        os.makedirs(os.path.join(d, size))
        for n in al.SHOT_ORDER:
            with open(os.path.join(d, size, f"{n}-dark.png"), "wb") as f:
                f.write(b"\x89PNG")
    return d


def do(mode, api, env=ENV, shots=None):
    lines = []
    al.run(api, mode, read("listing.md"), read("privacy-label.md"), "1.4.3", shots or shots_dir(), env, out=lines.append)
    return lines


class BuilderTests(unittest.TestCase):
    def setUp(self):
        self.s = _cl.sections(read("listing.md"))

    def test_first_version_sends_no_whats_new_a_later_one_does(self):
        self.assertNotIn("whatsNew", al.version_localization_attrs(self.s, True))
        self.assertEqual(al.version_localization_attrs(self.s, False)["whatsNew"], self.s["What's New"])

    def test_text_comes_from_listing_md(self):
        a = al.version_localization_attrs(self.s, True)
        self.assertEqual(a["keywords"], self.s["Keywords"])
        self.assertEqual(a["supportUrl"], "https://rivolt.dev/support")
        self.assertEqual(al.app_info_localization_attrs(self.s), {"subtitle": self.s["Subtitle"], "privacyPolicyUrl": "https://rivolt.dev/privacy"})

    def test_version_is_manual_release_with_the_llc_copyright(self):
        self.assertEqual(al.version_attrs(), {"copyright": "2026 ZERODOWNTIME LLC", "releaseType": "MANUAL"})

    def test_categories_are_utilities_then_travel(self):
        r = al.app_info_rel()
        self.assertEqual(r["primaryCategory"]["data"]["id"], "UTILITIES")
        self.assertEqual(r["secondaryCategory"]["data"]["id"], "TRAVEL")

    def test_age_rating_is_all_none(self):
        self.assertTrue(all(v in ("NONE", False) for v in al.AGE_NONE.values()))

    def test_us_only_and_free(self):
        p = al.availability_payload("APP")
        self.assertEqual([i["relationships"]["territory"]["data"]["id"] for i in p["included"]], ["USA"])
        self.assertFalse(p["data"]["attributes"]["availableInNewTerritories"])
        q = al.price_schedule_payload("APP", "pp0")
        self.assertEqual(q["included"][0]["relationships"]["appPricePoint"]["data"]["id"], "pp0")

    def test_review_details_need_every_secret_by_name(self):
        with self.assertRaises(SystemExit) as c:
            al.review_detail_attrs(self.s, {})
        self.assertIn("ASC_REVIEW_PASSWORD", str(c.exception))
        a = al.review_detail_attrs(self.s, ENV)
        self.assertEqual((a["demoAccountName"], a["notes"]), ("reviewer-user", self.s["Review notes"]))

    def test_click_list_has_the_seven_types_and_no_tracking(self):
        text = "\n".join(al.click_list(read("privacy-label.md")))
        for t in ("Email Address", "Name", "User ID", "Device ID", "Precise Location", "Other Financial Info", "Other Data Types"):
            self.assertIn(t, text)
        self.assertIn("Used for tracking = No", text)

    def test_missing_shots_fail_by_name(self):
        d = shots_dir()
        os.unlink(os.path.join(d, "6.3", "truck-dark.png"))
        with self.assertRaises(SystemExit) as c:
            al.screenshot_files(d)
        self.assertIn("truck-dark.png", str(c.exception))


class RunTests(unittest.TestCase):
    def test_plan_writes_nothing(self):
        api = FakeApi(versions=["1.4.3"])
        lines = do("plan", api)
        self.assertEqual(api.writes(), [])
        self.assertTrue(any(l.strip().startswith("WOULD PATCH /v1/appStoreVersions/V1.4.3") for l in lines))
        self.assertTrue(any("App Privacy" in l for l in lines))
        self.assertIn("No submission", lines[-1])

    def test_plan_never_prints_the_password_or_contact(self):
        out = "\n".join(do("plan", FakeApi(versions=["1.4.3"])))
        for secret in ("s3cret-pass", "reviewer-user", "rv@example.invalid", "+15550100"):
            self.assertNotIn(secret, out)

    def test_apply_creates_a_missing_version_and_never_submits(self):
        api = FakeApi(versions=[])
        do("apply", api)
        paths = [c[1] for c in api.writes()]
        self.assertIn("/v1/appStoreVersions", paths)
        self.assertFalse([p for p in paths if "reviewSubmission" in p or "appStoreVersionSubmissions" in p])
        post = next(c for c in api.writes() if c[:2] == ("POST", "/v1/appStoreVersions"))
        self.assertEqual(post[2]["data"]["attributes"]["versionString"], "1.4.3")
        self.assertNotIn("whatsNew", next(c for c in api.writes() if c[1].startswith("/v1/appStoreVersionLocalizations"))[2]["data"]["attributes"])

    def test_apply_sends_the_real_password_only_to_the_api(self):
        api = FakeApi(versions=["1.4.3"])
        do("apply", api)
        d = next(c for c in api.writes() if c[1] == "/v1/appStoreReviewDetails")
        self.assertEqual(d[2]["data"]["attributes"]["demoAccountPassword"], "s3cret-pass")

    def test_apply_replaces_screenshots_and_commits_each_upload(self):
        api = FakeApi(versions=["1.4.3"])
        do("apply", api)
        w = api.writes()
        self.assertEqual([c[1] for c in w if c[0] == "DELETE"], ["/v1/appScreenshots/old1", "/v1/appScreenshots/old2"])
        self.assertEqual(len([c for c in w if c[:2] == ("POST", "/v1/appScreenshots")]), 12)
        self.assertEqual(len([c for c in w if c[0] == "PUT"]), 12)
        self.assertEqual(len([c for c in w if c[0] == "PATCH" and c[1] == "/v1/appScreenshots/shot1"]), 12)

    def test_export_compliance_and_build_attach(self):
        api = FakeApi(versions=["1.4.3"])
        do("apply", api)
        b = next(c for c in api.writes() if c[1] == "/v1/builds/B1")
        self.assertIs(b[2]["data"]["attributes"]["usesNonExemptEncryption"], False)
        v = [c for c in api.writes() if c[1] == "/v1/appStoreVersions/V1.4.3" and "relationships" in c[2]["data"]]
        self.assertEqual(v[0][2]["data"]["relationships"]["build"]["data"]["id"], "B1")

    def test_an_unprocessed_build_stops_the_run_before_the_text(self):
        api = FakeApi(versions=["1.4.3"], build_state="PROCESSING")
        with self.assertRaises(SystemExit):
            do("apply", api)
        self.assertEqual(api.writes(), [])

    def test_a_listing_that_fails_its_checks_stops_everything(self):
        bad = read("listing.md").replace("https://rivolt.dev/support", "http://nope")
        api = FakeApi(versions=["1.4.3"])
        with self.assertRaises(SystemExit):
            al.run(api, "apply", bad, read("privacy-label.md"), "1.4.3", shots_dir(), ENV, out=lambda *_: None)
        self.assertEqual(api.calls, [])


if __name__ == "__main__":
    unittest.main()
