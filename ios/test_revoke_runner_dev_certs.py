"""Tests for revoke-runner-dev-certs.py's cleanup selection (stdlib only).

Run: python3 -I ios/test_revoke_runner_dev_certs.py
"""
import importlib.util
import os
import unittest

_spec = importlib.util.spec_from_file_location(
    "revoke_certs", os.path.join(os.path.dirname(os.path.abspath(__file__)), "revoke-runner-dev-certs.py"))
rc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(rc)

NOW = 2_000_000_000.0
OLD = NOW - 3 * 3600


def cert(cid, serial, name="Created via API", kind="DEVELOPMENT", nb=OLD):
    return {"id": cid, "nb": nb, "attributes": {"serialNumber": serial, "displayName": name, "certificateType": kind}}


def nb_of(c):
    return c["nb"]


CERTS = [
    cert("a", "7A1B2C4C654C"),
    cert("b", "0D9E8F227BE7"),
    cert("c", "11223355A974"),
    cert("person", "AABBCC998877", name="Jane Doe"),   # an Xcode certificate: never ours
    cert("young", "DDEEFF112233", nb=NOW - 600),       # made 10 minutes ago
    cert("dist", "445566778899", kind="DISTRIBUTION"),
    cert("dup1", "0000001234AB"),
    cert("dup2", "9999991234AB"),
]


class SelectListed(unittest.TestCase):
    def sel(self, suffixes):
        return rc.select_listed(CERTS, suffixes, NOW, nb_of=nb_of)

    def test_exact_suffixes_as_the_log_prints_them(self):
        ids, problems = self.sel(["…4C654C", "…227BE7", "55a974"])
        self.assertEqual(problems, [])
        self.assertEqual(ids, ["a", "b", "c"])

    def test_one_bad_suffix_revokes_nothing(self):
        for bad in (["…4C654C", "…998877"],   # a person's certificate
                    ["…4C654C", "…112233"],   # too young
                    ["…4C654C", "…778899"],   # distribution
                    ["…4C654C", "…FFFFFF"],   # no match
                    ["…4C654C", "1234AB"],    # matches two
                    ["…4C654C", "4C65"],      # too short
                    ["…4C654C", "ZZZZZZ"]):   # not hex
            ids, problems = self.sel(bad)
            self.assertEqual(ids, [], bad)
            self.assertTrue(problems, bad)

    def test_same_certificate_twice_is_refused(self):
        ids, problems = self.sel(["…4C654C", "7A1B2C4C654C"])
        self.assertEqual(ids, [])
        self.assertTrue(any("same certificate" in p for p in problems))

    def test_unreadable_not_before_is_refused(self):
        ids, problems = rc.select_listed([cert("x", "ABCDEF123456", nb=None)], ["123456"], NOW, nb_of=nb_of)
        self.assertEqual(ids, [])
        self.assertTrue(problems)


if __name__ == "__main__":
    unittest.main()
