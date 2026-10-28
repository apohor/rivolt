import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("check_listing", os.path.join(HERE, "check_listing.py"))
cl = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(cl)


def good():
    with open(os.path.join(HERE, "listing.md"), encoding="utf-8") as f:
        return f.read()


def replace_section(text, name, body):
    s = cl.sections(text)
    return text.replace(f"## {name}\n{s[name]}", f"## {name}\n{body}")


class ListingTests(unittest.TestCase):
    def test_the_committed_listing_is_clean(self):
        self.assertEqual(cl.check(good()), [])

    def test_over_a_limit_fails(self):
        for name, limit in cl.LIMITS.items():
            bad = replace_section(good(), name, "x" * (limit + 1))
            self.assertTrue(any(name in p and "limit" in p for p in cl.check(bad)), name)

    def test_at_the_limit_passes(self):
        for name, limit in (("Name", 30), ("Subtitle", 30), ("Promotional text", 170)):
            self.assertEqual(cl.check(replace_section(good(), name, "x" * limit)), [], name)

    def test_review_notes_never_carry_credentials(self):
        a = replace_section(good(), "Review notes", "Sign in as the test account, password hunter2.")
        self.assertTrue(any("password" in p for p in cl.check(a)))
        b = replace_section(good(), "Review notes", "Sign in as someone@example.com.")
        self.assertTrue(any("email" in p for p in cl.check(b)))
        # The description may say what the app never sees; only the review notes are checked.
        self.assertIn("password", cl.sections(good())["Description"])

    def test_keywords_have_no_spaces_after_commas(self):
        self.assertTrue(any("Keywords" in p for p in cl.check(replace_section(good(), "Keywords", "EV, truck"))))

    def test_a_missing_section_fails(self):
        self.assertTrue(any("Subtitle" in p for p in cl.check(good().replace("## Subtitle", "## Subtitl"))))

    def test_captions_keep_out_banned_words(self):
        bad = replace_section(good(), "Screenshot captions", "1. A secure view of your truck\n2. Exact numbers")
        probs = cl.check(bad)
        self.assertTrue(any("'secure'" in p for p in probs))
        self.assertTrue(any("'exact'" in p for p in probs))


if __name__ == "__main__":
    unittest.main()
