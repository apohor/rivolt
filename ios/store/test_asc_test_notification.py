import importlib.util
import os
import unittest

here = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("asc_test_notification", os.path.join(here, "asc_test_notification.py"))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class Verdict(unittest.TestCase):
    def test_first_result(self):
        self.assertEqual(m.verdict({"firstSendAttemptResult": "SUCCESS"}), "SUCCESS")

    def test_from_attempts(self):
        self.assertEqual(m.verdict({"sendAttempts": [{"sendAttemptResult": "TIMED_OUT"}]}), "TIMED_OUT")

    def test_none_yet(self):
        self.assertEqual(m.verdict({}), "NO_ATTEMPT_YET")

    def test_bid_claim_is_added(self):
        al = m._al()
        a = al.Api("k", "id", "iss", extra_claims={"bid": al.BUNDLE_ID})
        self.assertEqual(a.extra_claims, {"bid": al.BUNDLE_ID})
        self.assertEqual(al.Api("k", "id", "iss").extra_claims, {})


if __name__ == "__main__":
    unittest.main()
