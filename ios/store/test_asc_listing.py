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
       "ASC_REVIEW_CONTACT_LAST_NAME": "Viewer", "ASC_REVIEW_CONTACT_PHONE": "+15550100", "ASC_REVIEW_CONTACT_EMAIL": "rv@example.invalid", "EXPECT_BUILD_NUMBER": "109"}


def read(name):
    with open(os.path.join(HERE, name), encoding="utf-8") as f:
        return f.read()


SPEC = _spec_mod = None


def _load_spec():
    global SPEC
    if SPEC is None:
        SPEC = al._load("asc_spec").load(os.path.join(HERE, "asc-openapi-subset.json"))
    return SPEC


def fake_png(w, h):
    return b"\x89PNG\r\n\x1a\n" + b"\x00\x00\x00\rIHDR" + w.to_bytes(4, "big") + h.to_bytes(4, "big") + b"\x08\x06\x00\x00\x00"


class FakeApi:
    """Answers the GETs a run makes; records every call. versions: {versionString: state} already on the app."""

    def __init__(self, versions=(), build_state="VALID", loc=True, usa_only=False, free_price=False, put_status=200,
                 shot_state="COMPLETE", old_shots=None, encryption=None, attached=False, rights=None, build_number="109", build_marketing="1.4.3"):
        self.versions = dict(versions) if isinstance(versions, dict) else {v: "PREPARE_FOR_SUBMISSION" for v in versions}
        self.build_state, self.loc, self.usa_only, self.free_price = build_state, loc, usa_only, free_price
        self.put_status, self.shot_state, self.calls = put_status, shot_state, []
        self.encryption, self.attached, self.rights = encryption, attached, rights
        self.build_number, self.build_marketing = build_number, build_marketing
        self.old_shots = old_shots if old_shots is not None else [("old1", "x1"), ("old2", "x2")]

    def put_bytes(self, url, headers, chunk):
        self.calls.append(("PUT", url, len(chunk)))
        return self.put_status

    def request(self, method, path, body=None):
        self.calls.append((method, path, body))
        if method != "GET":
            if path == "/v1/appScreenshots" and method == "POST":
                return 201, {"data": {"id": "shot1", "attributes": {"uploadOperations": [
                    {"offset": 0, "length": 4, "url": "https://up.example.invalid/x", "requestHeaders": [{"name": "Content-Type", "value": "image/png"}]}]}}}
            return 200, {"data": {"id": "new-" + path.split("/")[-1], "attributes": {}}}
        if path.startswith("/v1/apps?filter"):
            return 200, {"data": [{"id": "APP", "attributes": ({} if self.rights is None else {"contentRightsDeclaration": self.rights})}]}
        if "/appPricePoints" in path:
            return 200, {"data": [{"id": "pp0", "attributes": {"customerPrice": "0.0"}}, {"id": "pp1", "attributes": {"customerPrice": "0.99"}}]}
        if path.endswith("/appAvailabilityV2"):
            return (200, {"data": {"id": "AV1"}}) if self.usa_only is not None else (404, {})
        if path.startswith("/v2/appAvailabilities/AV1/territoryAvailabilities"):
            on = ["USA"] if self.usa_only else ["USA", "DEU"]
            return 200, {"data": [{"id": "t" + x, "attributes": {"available": True}, "relationships": {"territory": {"data": {"id": x}}}} for x in on]}
        if path.endswith("/appPriceSchedule"):
            return (200, {"data": {"id": "PS1"}}) if self.free_price else (404, {})
        if path.startswith("/v1/appPriceSchedules/PS1/manualPrices"):
            return 200, {"data": [], "included": [{"type": "appPricePoints", "id": "pp0", "attributes": {"customerPrice": "0.0"}}]}
        if path.endswith("/appStoreVersions?filter[platform]=IOS&limit=200&include=build"):
            rel_build = {"build": {"data": {"type": "builds", "id": "B1"}}} if self.attached else {"build": {"data": None}}
            return 200, {"data": [{"id": "V" + v, "attributes": {"versionString": v, "appVersionState": s}, "relationships": rel_build} for v, s in self.versions.items()]}
        if path.startswith("/v1/builds"):
            return 200, {"included": [{"type": "preReleaseVersions", "id": "P1", "attributes": {"version": self.build_marketing}}], "data": [{"id": "B1", "attributes": dict({"processingState": self.build_state, "version": self.build_number}, **({} if self.encryption is None else {"usesNonExemptEncryption": self.encryption}))}]}
        if path.endswith("/appStoreVersionLocalizations"):
            return 200, {"data": [{"id": "L1", "attributes": {"locale": "en-US"}}] if self.loc else []}
        if path.endswith("/appInfos"):
            return 200, {"data": [{"id": "I1", "attributes": {"state": "PREPARE_FOR_SUBMISSION"}}]}
        if path.endswith("/appInfoLocalizations"):
            return 200, {"data": [{"id": "IL1", "attributes": {"locale": "en-US"}}]}
        if path.endswith("/ageRatingDeclaration"):
            return 200, {"data": {"id": "AG1"}}
        if path.endswith("/appStoreReviewDetail"):
            return 200, {"data": None}
        if path.endswith("/appScreenshotSets"):
            return 200, {"data": [{"id": "S67", "attributes": {"screenshotDisplayType": "APP_IPHONE_67"}}]}
        if path == "/v1/appScreenshots/shot1":
            return 200, {"data": {"id": "shot1", "attributes": {"assetDeliveryState": {"state": self.shot_state, "errors": []}}}}
        if path.endswith("/appScreenshots"):
            return 200, {"data": [{"id": i, "attributes": {"sourceFileChecksum": c}} for i, c in self.old_shots]}
        raise AssertionError("unexpected GET " + path)

    def writes(self):
        return [c for c in self.calls if c[0] in ("POST", "PATCH", "DELETE", "PUT")]


def shots_dir(px=None):
    d = tempfile.mkdtemp()
    for size in al.DISPLAY_TYPES:
        os.makedirs(os.path.join(d, size))
        for n in al.SHOT_ORDER:
            with open(os.path.join(d, size, f"{n}-dark.png"), "wb") as f:
                f.write(fake_png(*(px or al.SHOT_PIXELS[size])))
    return d


def do(mode, api, env=ENV, shots=None, lines=None):
    lines = [] if lines is None else lines
    al.run(api, mode, read("listing.md"), read("privacy-label.md"), "1.4.3", shots or shots_dir(), env, out=lines.append, spec=_load_spec())
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
        self.assertIn("ASC_REVIEW_CONTACT_PHONE", str(c.exception))
        a = al.review_detail_attrs(self.s, ENV)
        self.assertEqual((a["demoAccountName"], a["notes"]), ("reviewer-user", self.s["Review notes"]))

    def test_no_review_account_means_demo_only_and_no_credentials(self):
        env = {k: v for k, v in ENV.items() if k not in ("ASC_REVIEW_USER", "ASC_REVIEW_PASSWORD")}
        a = al.review_detail_attrs(self.s, env)
        self.assertIs(a["demoAccountRequired"], False)
        self.assertFalse({"demoAccountName", "demoAccountPassword"} & set(a))
        self.assertEqual(a["contactEmail"], "rv@example.invalid")
        with self.assertRaises(SystemExit):
            al.review_detail_attrs(self.s, dict(env, ASC_REVIEW_USER="u"))
        with self.assertRaises(SystemExit):
            al.review_detail_attrs(self.s, {k: v for k, v in env.items() if k != "ASC_REVIEW_CONTACT_PHONE"})

    def test_review_notes_lead_with_the_demo_truck(self):
        self.assertTrue(self.s["Review notes"].split("Tap ")[1].startswith('"Try the demo truck"'))
        self.assertNotIn("test account", self.s["Review notes"])

    def test_click_list_has_the_seven_types_and_no_tracking(self):
        text = "\n".join(al.click_list(read("privacy-label.md")))
        for t in ("Email Address", "Name", "User ID", "Device ID", "Precise Location", "Other Financial Info", "Other Data Types"):
            self.assertIn(t, text)
        self.assertIn("Used for tracking = No", text)

    def test_missing_shots_fail_by_name(self):
        d = shots_dir()
        os.unlink(os.path.join(d, "6.9", "truck-dark.png"))
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

    def test_apply_uploads_the_new_shots_before_it_removes_the_old(self):
        api = FakeApi(versions=["1.4.3"])
        do("apply", api)
        w = api.writes()
        self.assertEqual([c[1] for c in w if c[0] == "DELETE"], ["/v1/appScreenshots/old1", "/v1/appScreenshots/old2"])
        self.assertEqual(len([c for c in w if c[:2] == ("POST", "/v1/appScreenshots")]), 6)
        self.assertEqual(len([c for c in w if c[0] == "PUT"]), 6)
        self.assertEqual(len([c for c in w if c[0] == "PATCH" and c[1] == "/v1/appScreenshots/shot1"]), 6)
        first_delete = next(i for i, c in enumerate(w) if c[0] == "DELETE")
        last_commit = max(i for i, c in enumerate(w) if c[0] == "PATCH" and c[1] == "/v1/appScreenshots/shot1")
        self.assertLess(last_commit, first_delete)

    def test_a_full_set_never_goes_over_ten_shots(self):
        api = FakeApi(versions=["1.4.3"], old_shots=[(f"old{i}", f"x{i}") for i in range(6)])
        do("apply", api)
        size, peak = 6, 6
        for c in api.writes():
            if c[:2] == ("POST", "/v1/appScreenshots"):
                size += 1
            elif c[0] == "DELETE":
                size -= 1
            peak = max(peak, size)
        self.assertEqual(size, 6)
        self.assertLessEqual(peak, 10)
        self.assertEqual(len([c for c in api.writes() if c[0] == "DELETE"]), 6)

    def test_a_set_that_already_holds_these_shots_is_left_alone(self):
        d = shots_dir()
        import hashlib
        same = []
        for n in al.SHOT_ORDER:
            with open(os.path.join(d, "6.9", f"{n}-dark.png"), "rb") as f:
                same.append((f"s-{n}", hashlib.md5(f.read()).hexdigest()))
        api = FakeApi(versions=["1.4.3"], old_shots=same)
        lines = do("apply", api, shots=d)
        self.assertFalse([c for c in api.writes() if c[1].startswith("/v1/appScreenshots") or c[0] == "PUT"])
        self.assertTrue(any("already holds exactly" in l for l in lines))

    def test_a_failure_says_what_is_left_in_the_set(self):
        api = FakeApi(versions=["1.4.3"], old_shots=[(f"old{i}", f"x{i}") for i in range(6)], put_status=500)
        with self.assertRaises(SystemExit) as c:
            do("apply", api)
        self.assertIn("now holds 6 old and 0 new", str(c.exception))

    def test_a_failed_upload_leaves_the_old_shots(self):
        api = FakeApi(versions=["1.4.3"], put_status=403)
        with self.assertRaises(SystemExit):
            do("apply", api)
        self.assertFalse([c for c in api.writes() if c[0] == "DELETE"])

    def test_a_shot_apple_rejects_leaves_the_old_shots(self):
        api = FakeApi(versions=["1.4.3"], shot_state="FAILED")
        with self.assertRaises(SystemExit) as c:
            do("apply", api)
        self.assertIn("rejected", str(c.exception))
        self.assertFalse([c for c in api.writes() if c[0] == "DELETE"])

    def test_the_plan_prints_each_shot_size_and_sha256(self):
        lines = "\n".join(do("plan", FakeApi(versions=["1.4.3"])))
        self.assertIn("shot 6.9/truck-dark.png 1320x2868 sha256 ", lines)

    def test_a_shot_of_the_wrong_size_stops_before_any_write(self):
        api = FakeApi(versions=["1.4.3"])
        with self.assertRaises(SystemExit) as c:
            do("apply", api, shots=shots_dir(px=(1206, 2622)))
        self.assertIn("1206x2622", str(c.exception))
        self.assertEqual(api.writes(), [])

    def test_a_missing_secret_or_shot_stops_the_apply_before_the_first_write(self):
        for kwargs in ({"env": {}}, {"shots": tempfile.mkdtemp()}):
            api = FakeApi(versions=[])
            with self.assertRaises(SystemExit):
                do("apply", api, **kwargs)
            self.assertEqual(api.writes(), [], kwargs.keys())

    def test_availability_and_price_are_skipped_when_already_set(self):
        api = FakeApi(versions=["1.4.3"], usa_only=True, free_price=True)
        do("apply", api)
        paths = [c[1] for c in api.writes()]
        self.assertNotIn("/v2/appAvailabilities", paths)
        self.assertNotIn("/v1/appPriceSchedules", paths)

    def test_availability_is_posted_when_other_territories_are_on(self):
        api = FakeApi(versions=["1.4.3"], usa_only=False)
        do("apply", api)
        self.assertEqual([c[1] for c in api.writes()].count("/v2/appAvailabilities"), 1)

    def test_another_editable_version_stops_the_run_before_any_write(self):
        api = FakeApi(versions={"1.3.0": "READY_FOR_DISTRIBUTION", "1.4.0": "PREPARE_FOR_SUBMISSION"})
        with self.assertRaises(SystemExit) as c:
            do("apply", api)
        self.assertIn("1.4.0", str(c.exception))
        self.assertEqual(api.writes(), [])

    def test_a_never_released_apps_only_editable_version_is_renumbered(self):
        api = FakeApi(versions={"1.0": "PREPARE_FOR_SUBMISSION"})
        lines = []
        do("apply", api, lines=lines)
        patch = next(c for c in api.writes() if c[0] == "PATCH" and c[1] == "/v1/appStoreVersions/V1.0" and "attributes" in c[2]["data"])
        self.assertEqual(patch[2]["data"]["attributes"]["versionString"], "1.4.3")
        self.assertNotIn("/v1/appStoreVersions", [c[1] for c in api.writes() if c[0] == "POST"])
        self.assertTrue(any("becomes 1.4.3" in x for x in lines))
        attrs = next(c for c in api.writes() if c[1].startswith("/v1/appStoreVersionLocalizations"))[2]["data"]["attributes"]
        self.assertNotIn("whatsNew", attrs)

    def test_a_released_version_next_to_an_editable_one_still_stops(self):
        for vs in ({"1.0": "PREPARE_FOR_SUBMISSION", "1.1": "READY_FOR_DISTRIBUTION"}, {"1.0": "PREPARE_FOR_SUBMISSION", "1.1": "PREPARE_FOR_SUBMISSION"},
                   {"1.0": "WAITING_FOR_REVIEW"}, {"1.0": "IN_REVIEW"}, {"1.0": "REJECTED"}, {"1.0": "READY_FOR_REVIEW"}):
            api = FakeApi(versions=vs)
            with self.assertRaises(SystemExit):
                do("apply", api)
            self.assertEqual(api.writes(), [])

    def test_the_plan_says_the_light_shots_are_not_uploaded(self):
        lines = []
        do("plan", FakeApi(versions=["1.4.3"]), lines=lines)
        self.assertTrue(any("light shots are not uploaded" in x for x in lines))

    def test_export_compliance_already_set_is_skipped_and_said(self):
        api, lines = FakeApi(versions=["1.4.3"], encryption=False), []
        do("apply", api, lines=lines)
        self.assertNotIn("/v1/builds/B1", [c[1] for c in api.writes()])
        self.assertTrue(any("export compliance is already answered" in x for x in lines))

    def test_the_plan_prints_the_build_and_a_different_build_number_stops(self):
        lines = []
        do("plan", FakeApi(versions=["1.4.3"]), lines=lines)
        self.assertTrue(any("version 1.4.3, build number 109" in x for x in lines))
        for kw in ({"build_number": "110"}, {"build_marketing": "1.4.2"}):
            api = FakeApi(versions=["1.4.3"], **kw)
            with self.assertRaises(SystemExit) as c:
                do("apply", api)
            self.assertIn("stopping", str(c.exception))
            self.assertEqual(api.writes(), [])
        with self.assertRaises(SystemExit):
            do("plan", FakeApi(versions=["1.4.3"]), env=dict(ENV, EXPECT_BUILD_NUMBER=""))

    def test_a_build_that_uses_encryption_stops_before_any_write(self):
        api = FakeApi(versions=["1.4.3"], encryption=True)
        with self.assertRaises(SystemExit) as c:
            do("apply", api)
        self.assertIn("usesNonExemptEncryption is true", str(c.exception))
        self.assertEqual(api.writes(), [])

    def test_a_rerun_after_a_partial_apply_repeats_nothing_that_was_done(self):
        # Rights set, price free, US only, version already 1.4.3 with the build attached and the export answer set.
        api, lines = FakeApi(versions=["1.4.3"], encryption=False, attached=True, rights="USES_THIRD_PARTY_CONTENT", usa_only=True, free_price=True), []
        do("apply", api, lines=lines)
        done = [(c[0], c[1]) for c in api.writes()]
        self.assertNotIn(("PATCH", "/v1/apps/APP"), done)
        self.assertNotIn(("PATCH", "/v1/builds/B1"), done)
        self.assertNotIn("/v1/appPriceSchedules", [c[1] for c in api.writes()])
        self.assertNotIn("/v2/appAvailabilities", [c[1] for c in api.writes()])
        self.assertFalse([c for c in api.writes() if c[1] == "/v1/appStoreVersions/V1.4.3" and "relationships" in c[2]["data"]])
        self.assertTrue(any("already attached" in x for x in lines))

    def test_whats_new_goes_out_only_when_an_earlier_version_exists(self):
        api = FakeApi(versions={"1.4.0": "READY_FOR_DISTRIBUTION"})
        do("apply", api)
        attrs = next(c for c in api.writes() if c[1].startswith("/v1/appStoreVersionLocalizations"))[2]["data"]["attributes"]
        self.assertIn("whatsNew", attrs)

    def test_every_call_matches_the_spec_and_a_wrong_body_is_caught(self):
        sp = _load_spec()
        v = al._load("asc_spec").validate_call
        self.assertIn("APP_IPHONE_69", v(sp, "POST", "/v1/appScreenshotSets", {"data": {"type": "appScreenshotSets", "attributes": {"screenshotDisplayType": "APP_IPHONE_69"},
                         "relationships": {"appStoreVersionLocalization": al.rel("appStoreVersionLocalizations", "L")}}})[0])
        self.assertTrue(v(sp, "PATCH", "/v1/ageRatingDeclarations/AG1", {"data": {"type": "ageRatingDeclarations", "id": "AG1", "attributes": {"madeUp": True}}}))
        self.assertTrue(v(sp, "GET", "/v1/apps?filter[nope]=1"))

    def test_age_rating_answers_every_questionnaire_field(self):
        props = _load_spec()["components"]["schemas"]["AgeRatingDeclarationUpdateRequest"]["properties"]["data"]["properties"]["attributes"]["properties"]
        skip = {"kidsAgeBand", "ageRatingOverride", "ageRatingOverrideV2", "koreaAgeRatingOverride", "gracRatingClassificationNumber",
                "developerAgeRatingInfoUrl", "socialMediaAgeRestricted"}
        self.assertEqual(sorted(set(props) - skip - set(al.AGE_NONE)), [])

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
