"""Tests for apple-team-check.py's decisions (stdlib only, no network, no signing).

Run: python3 -I ios/test_apple_team_check.py
"""
import importlib.util
import os
import unittest

_spec = importlib.util.spec_from_file_location(
    "team_check", os.path.join(os.path.dirname(os.path.abspath(__file__)), "apple-team-check.py"))
tc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(tc)

B = "dev.rivolt.app"
TEAM = "ABCDE12345"


def doc(*items):
    return {"data": [{"attributes": {"identifier": i, "seedId": s}} for i, s in items]}


class Classify(unittest.TestCase):
    def test_same_team_is_ok(self):
        self.assertEqual(tc.classify(200, doc((B, TEAM)), B, TEAM)[0], "ok")

    def test_other_team_fails_and_names_both(self):
        level, msg = tc.classify(200, doc((B, "ZZZZZ99999")), B, TEAM)
        self.assertEqual(level, "fail")
        self.assertIn("ZZZZZ99999", msg)
        self.assertIn(TEAM, msg)

    def test_prefix_match_is_not_the_bundle(self):
        # filter[identifier] also returns dev.rivolt.app.widgets: only the exact id counts.
        level, _ = tc.classify(200, doc((B + ".widgets", "ZZZZZ99999")), B, TEAM)
        self.assertEqual(level, "warn")

    def test_refused_key_fails(self):
        level, msg = tc.classify(401, {"errors": [{"code": "NOT_AUTHORIZED", "title": "Authentication credentials are missing or invalid."}]}, B, TEAM)
        self.assertEqual(level, "fail")
        self.assertIn("NOT_AUTHORIZED", msg)

    def test_unclear_answers_never_block(self):
        for status in (0, 403, 429, 500):
            self.assertEqual(tc.classify(status, None, B, TEAM)[0], "warn", status)

    def test_missing_seed_is_a_warning(self):
        self.assertEqual(tc.classify(200, {"data": [{"attributes": {"identifier": B}}]}, B, TEAM)[0], "warn")

    def test_error_detail_is_not_echoed(self):
        _, msg = tc.classify(403, {"errors": [{"code": "FORBIDDEN", "title": "t", "detail": "SECRET-DETAIL"}]}, B, TEAM)
        self.assertNotIn("SECRET-DETAIL", msg)


def bundle(bid, team):
    return {"bundle_id": bid, "version": "1.0 (1)", "team": team, "entitlements": {}}


class Verdict(unittest.TestCase):
    def test_all_same_team(self):
        bs = [bundle(B, TEAM), bundle(B + ".widgets", TEAM)]
        self.assertEqual(tc.verdict(bs, TEAM), [])

    def test_mixed_teams(self):
        self.assertTrue(tc.verdict([bundle(B, TEAM), bundle(B + ".widgets", "ZZZZZ99999")]))

    def test_wrong_expected_team(self):
        self.assertTrue(tc.verdict([bundle(B, TEAM)], "ZZZZZ99999"))

    def test_unsigned(self):
        self.assertTrue(tc.verdict([bundle(B, None)]))


class Describe(unittest.TestCase):
    def test_keeps_only_team_bound_entitlements(self):
        ents = {"application-identifier": TEAM + "." + B, "get-task-allow": False, "aps-environment": "production",
                "com.apple.security.application-groups": ["group.dev.rivolt.app"]}
        d = tc.describe({"CFBundleIdentifier": B, "CFBundleShortVersionString": "1.3.1", "CFBundleVersion": "7"}, ents, TEAM)
        self.assertNotIn("get-task-allow", d["entitlements"])
        self.assertEqual(d["entitlements"]["aps-environment"], "production")
        self.assertEqual(d["version"], "1.3.1 (7)")


class Der(unittest.TestCase):
    def test_der_to_raw_pads_to_32_bytes(self):
        r, s = b"\x01" * 31, b"\x80" + b"\x02" * 31
        der = b"\x30\x45\x02\x1f" + r + b"\x02\x20" + s
        raw = tc.der_to_raw(der)
        self.assertEqual(len(raw), 64)
        self.assertEqual(raw[:32], b"\x00" + r)
        self.assertEqual(raw[32:], s)


if __name__ == "__main__":
    unittest.main()
