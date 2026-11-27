import hashlib
import importlib.util
import os
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))


def _mod(name):
    s = importlib.util.spec_from_file_location(name, os.path.join(HERE, name + ".py"))
    m = importlib.util.module_from_spec(s)
    s.loader.exec_module(m)
    return m


sub = _mod("asc_subscriptions")
SPEC = _mod("asc_spec").load(os.path.join(HERE, "asc-openapi-subset.json"))
with open(os.path.join(HERE, "subscriptions.md"), encoding="utf-8") as _f:
    MD = _f.read()


def png(w=1320, h=2868):
    return b"\x89PNG\r\n\x1a\n" + b"\x00\x00\x00\rIHDR" + w.to_bytes(4, "big") + h.to_bytes(4, "big") + b"\x08\x06\x00\x00\x00"


def shot_file(data=None):
    f = tempfile.NamedTemporaryFile("wb", suffix=".png", delete=False)
    f.write(data or png())
    f.close()
    return f.name


POINTS = [{"id": "p399", "attributes": {"customerPrice": "3.99"}}, {"id": "p349", "attributes": {"customerPrice": "34.99"}},
          {"id": "p499", "attributes": {"customerPrice": "4.99"}}]


class FakeApi:
    """group: None, or exists. products: {productId: {period, price, avail, loc, shot_md5}} already in the group."""

    def __init__(self, group=False, products=None, points=POINTS, app_attrs=None, group_loc=True, next_page=False):
        self.group, self.products, self.points = group, products or {}, points
        self.app_attrs, self.group_loc, self.next_page, self.calls = app_attrs or {}, group_loc, next_page, []
        self.ids = {}

    def put_bytes(self, url, headers, chunk):
        self.calls.append(("PUT", url, len(chunk)))
        return 200

    def _sid(self, pid):
        return "S-" + pid.rsplit(".", 1)[1]

    def request(self, method, path, body=None):
        self.calls.append((method, path, body))
        if method != "GET":
            if path == "/v1/subscriptionAppStoreReviewScreenshots" and method == "POST":
                return 201, {"data": {"id": "rs1", "attributes": {"uploadOperations": [{"offset": 0, "length": 4, "url": "https://up.example.invalid/x", "requestHeaders": [{"name": "Content-Type", "value": "image/png"}]}]}}}
            return 201, {"data": {"id": "new-" + path.split("/")[-1], "attributes": {}}}
        if path.startswith("/v1/apps?filter"):
            return 200, {"data": [{"id": "APP", "attributes": self.app_attrs}]}
        if path.startswith("/v1/apps/APP/subscriptionGroups"):
            return 200, {"data": [{"id": "G1", "attributes": {}}] if self.group else []}
        if path.endswith("/subscriptionGroupLocalizations"):
            return 200, {"data": [{"id": "GL1", "attributes": {"locale": "en-US", "name": "Rivolt Pro"}}] if self.group_loc else []}
        if path.startswith("/v1/subscriptionGroups/G1/subscriptions"):
            return 200, {"data": [{"id": self._sid(pid), "attributes": {"productId": pid, "subscriptionPeriod": p["period"], "name": p.get("name", ""), "groupLevel": p.get("level"), "familySharable": False, "reviewNote": p.get("note")}} for pid, p in self.products.items()]}
        if path.startswith("/v1/subscriptionPlanAvailabilities/PA-"):
            key = path.split("/")[3][3:]
            return 200, {"data": [{"id": t} for t in next(v for k, v in self.products.items() if k.endswith("." + key))["avail"]]}
        pid = next((k for k in self.products if path.startswith(f"/v1/subscriptions/{self._sid(k)}/")), None)
        if pid is None:
            raise AssertionError("unexpected GET " + path)
        p = self.products[pid]
        if path.endswith("/subscriptionLocalizations"):
            return 200, {"data": [{"id": "SL", "attributes": {"locale": "en-US", "name": "Rivolt Pro " + pid.rsplit(".", 1)[1].capitalize(), "description": "Full history, trip planning and every vehicle"}}] if p.get("loc") else []}
        if path.endswith("/planAvailabilities"):
            return 200, {"data": [{"id": "PA-" + pid.rsplit(".", 1)[1], "attributes": {"planType": "UPFRONT"}}] if p.get("avail") else []}
        if path.startswith("/v1/subscriptionPlanAvailabilities/PA/availableTerritories"):
            return 200, {"data": [{"id": t} for t in p["avail"]]}
        if "/pricePoints" in path:
            doc = {"data": self.points}
            if self.next_page:
                doc["links"] = {"next": "https://x.invalid/next"}
            return 200, doc
        if "/prices?" in path:
            if not p.get("price"):
                return 200, {"data": []}
            return 200, {"data": [], "included": [{"type": "subscriptionPricePoints", "id": "pp", "attributes": {"customerPrice": p["price"]}}]}
        if path.endswith("/appStoreReviewScreenshot"):
            return (200, {"data": {"id": "RS0", "attributes": {"sourceFileChecksum": p["shot_md5"]}}}) if p.get("shot_md5") else (200, {"data": None})
        raise AssertionError("unexpected GET " + path)

    def writes(self):
        return [c for c in self.calls if c[0] in ("POST", "PATCH", "DELETE", "PUT")]


def do(mode, api, shot=None, md=MD, lines=None):
    lines = [] if lines is None else lines
    sub.run(api, mode, md, shot, out=lines.append, spec=SPEC)
    return lines


def full(pid, **kw):
    period = "ONE_YEAR" if pid.endswith("yearly") else "ONE_MONTH"
    price = "34.99" if pid.endswith("yearly") else "3.99"
    name = "Rivolt Pro " + pid.rsplit(".", 1)[1].capitalize()
    d = {"period": period, "price": price, "avail": ["USA"], "loc": True, "name": name, "level": 1 if pid.endswith("yearly") else 2,
         "note": sub.parse(MD)["Review"]["Notes"]}
    d.update(kw)
    return d


class DesiredState(unittest.TestCase):
    def test_the_file_passes_its_own_checks(self):
        self.assertEqual(sub.check(sub.parse(MD)), [])

    def test_prices_ids_and_urls_are_the_decided_ones(self):
        d = sub.parse(MD)
        self.assertEqual((d["Monthly"]["Price"], d["Yearly"]["Price"]), ("3.99", "34.99"))
        self.assertEqual((d["Monthly"]["Product ID"], d["Yearly"]["Product ID"]), ("dev.rivolt.app.pro.monthly", "dev.rivolt.app.pro.yearly"))
        self.assertEqual((d["Monthly"]["Period"], d["Yearly"]["Period"]), ("ONE_MONTH", "ONE_YEAR"))
        self.assertEqual(d["Notifications"]["Production URL"], "https://rivolt.dev/api/billing/apple/notifications")
        self.assertTrue(d["Notifications"]["Sandbox URL"].startswith("https://preview.rivolt.dev/"))
        self.assertEqual(d["Notifications"]["Version"], "V2")

    def test_the_review_note_has_no_price_or_link_to_a_payment_page(self):
        n = sub.parse(MD)["Review"]["Notes"]
        self.assertNotIn("$", n)
        self.assertNotIn("stripe", n.lower())

    def test_bad_input_is_named(self):
        d = sub.parse(MD)
        d["Monthly"]["Price"] = "3.9"
        d["Yearly"]["Description"] = "x" * 46
        d["Notifications"]["Sandbox URL"] = "http://x"
        p = " ".join(sub.check(d))
        self.assertIn("Monthly: Price", p)
        self.assertIn("Yearly: description", p)
        self.assertIn("Sandbox URL must be https", p)
        self.assertTrue(sub.check({}))


class PlanTests(unittest.TestCase):
    def test_a_fresh_plan_writes_nothing_and_every_call_matches_the_spec(self):
        api = FakeApi()
        lines = do("plan", api, shot_file())
        self.assertEqual([c for c in api.calls if c[0] != "GET"], [])
        text = "\n".join(lines)
        for want in ("PATCH /v1/apps/APP", "POST /v1/subscriptionGroups", "POST /v1/subscriptionGroupLocalizations", "POST /v1/subscriptions",
                     "POST /v1/subscriptionLocalizations", "POST /v1/subscriptionPlanAvailabilities", "POST /v1/subscriptionAppStoreReviewScreenshots"):
            self.assertIn(want, text)
        self.assertIn("subscriptionStatusUrlForSandbox", text)
        self.assertIn("price point can be looked up only once the product exists", text)

    def test_apply_is_not_on_the_command_line(self):
        with self.assertRaises(SystemExit):
            sub.main(["apply", "--spec", "x"])

    def test_a_plan_with_no_screenshot_says_it_is_still_to_do(self):
        text = "\n".join(do("plan", FakeApi()))
        self.assertIn("no --review-shot given", text)
        self.assertNotIn("subscriptionAppStoreReviewScreenshots", text)

    def test_a_tiny_screenshot_stops_before_any_write(self):
        api = FakeApi()
        with self.assertRaises(SystemExit):
            do("plan", api, shot_file(png(100, 100)))
        self.assertEqual(api.writes(), [])

    def test_everything_already_set_is_skipped(self):
        data = png()
        md5 = hashlib.md5(data).hexdigest()
        n = sub.parse(MD)["Notifications"]
        api = FakeApi(group=True, products={p: full(p, shot_md5=md5) for p in ("dev.rivolt.app.pro.monthly", "dev.rivolt.app.pro.yearly")},
                      app_attrs=sub.app_urls_attrs(n))
        lines = do("plan", api, shot_file(data))
        self.assertEqual(api.writes(), [])
        self.assertEqual([l for l in lines if l.strip().startswith(("WOULD", "POST", "PATCH"))], [])

    def test_an_existing_product_gets_its_price_from_the_exact_point(self):
        api = FakeApi(group=True, products={"dev.rivolt.app.pro.monthly": full("dev.rivolt.app.pro.monthly", price=None),
                                            "dev.rivolt.app.pro.yearly": full("dev.rivolt.app.pro.yearly", price=None)})
        do("plan", api)
        posts = [c for c in api.calls if c[1] == "/v1/subscriptionPrices"]
        self.assertEqual(len(posts), 0)  # a plan never writes
        lines = "\n".join(do("plan", api))
        self.assertIn("POST /v1/subscriptionPrices", lines)
        self.assertIn('"id": "p349"', lines)
        self.assertIn('"id": "p399"', lines)


class ApplyTests(unittest.TestCase):
    def test_apply_creates_in_order_and_prices_at_the_exact_points(self):
        api = FakeApi()
        real = api.request

        def req(method, path, body=None):
            # a product the run just created: Apple answers its price point list
            if method == "GET" and "/pricePoints" in path and "/new-" in path:
                api.calls.append((method, path, body))
                return 200, {"data": POINTS}
            if method == "POST" and path == "/v1/subscriptions":
                api.calls.append((method, path, body))
                return 201, {"data": {"id": "new-" + body["data"]["attributes"]["productId"].rsplit(".", 1)[1], "attributes": {}}}
            return real(method, path, body)

        api.request = req
        sub.run(api, "apply", MD, shot_file(), out=lambda *_: None, spec=SPEC)
        w = api.writes()
        paths = [c[1] for c in w if c[0] != "PUT"]
        self.assertLess(paths.index("/v1/subscriptionGroups"), paths.index("/v1/subscriptions"))
        self.assertLess(paths.index("/v1/subscriptions"), paths.index("/v1/subscriptionPrices"))
        prices = [c[2]["data"]["relationships"]["subscriptionPricePoint"]["data"]["id"] for c in w if c[1] == "/v1/subscriptionPrices"]
        self.assertEqual(sorted(prices), ["p349", "p399"])
        self.assertTrue(any(c[0] == "PUT" for c in w))
        self.assertFalse([c for c in w if "submission" in c[1].lower()])

    def test_no_exact_price_point_stops_and_prices_nothing(self):
        pts = [p for p in POINTS if p["id"] != "p399"]
        api = FakeApi(group=True, products={p: full(p, price=None) for p in ("dev.rivolt.app.pro.monthly", "dev.rivolt.app.pro.yearly")}, points=pts)
        with self.assertRaises(SystemExit) as e:
            do("plan", api)
        self.assertIn("exactly one", str(e.exception))
        self.assertEqual(api.writes(), [])

    def test_two_points_at_one_price_also_stop(self):
        with self.assertRaises(SystemExit):
            sub.exact_point(POINTS + [{"id": "dup", "attributes": {"customerPrice": "3.99"}}], "3.99")

    def test_a_different_existing_price_stops(self):
        api = FakeApi(group=True, products={"dev.rivolt.app.pro.monthly": full("dev.rivolt.app.pro.monthly", price="4.99"),
                                            "dev.rivolt.app.pro.yearly": full("dev.rivolt.app.pro.yearly")})
        with self.assertRaises(SystemExit) as e:
            do("plan", api)
        self.assertIn("price change", str(e.exception))

    def test_a_different_period_stops(self):
        api = FakeApi(group=True, products={"dev.rivolt.app.pro.monthly": full("dev.rivolt.app.pro.monthly", period="ONE_YEAR")})
        with self.assertRaises(SystemExit) as e:
            do("plan", api)
        self.assertIn("period cannot change", str(e.exception))

    def test_other_territories_stop(self):
        api = FakeApi(group=True, products={"dev.rivolt.app.pro.monthly": full("dev.rivolt.app.pro.monthly", avail=["USA", "DEU"]),
                                            "dev.rivolt.app.pro.yearly": full("dev.rivolt.app.pro.yearly")})
        with self.assertRaises(SystemExit):
            do("plan", api)

    def test_a_second_page_of_price_points_stops(self):
        api = FakeApi(group=True, next_page=True, products={"dev.rivolt.app.pro.monthly": full("dev.rivolt.app.pro.monthly", price=None)})
        with self.assertRaises(SystemExit):
            do("plan", api)

    def test_a_changed_review_note_is_patched_not_recreated(self):
        api = FakeApi(group=True, products={p: full(p, note="old") for p in ("dev.rivolt.app.pro.monthly", "dev.rivolt.app.pro.yearly")})
        text = "\n".join(do("plan", api))
        self.assertIn("update reviewNote", text)
        self.assertNotIn("POST /v1/subscriptions ", text)

    def test_a_changed_screenshot_replaces_the_old_one(self):
        api = FakeApi(group=True, products={p: full(p, shot_md5="old") for p in ("dev.rivolt.app.pro.monthly", "dev.rivolt.app.pro.yearly")})
        text = "\n".join(do("plan", api, shot_file()))
        self.assertIn("DELETE /v1/subscriptionAppStoreReviewScreenshots/RS0", text)
        self.assertIn("POST /v1/subscriptionAppStoreReviewScreenshots", text)


class SpecTests(unittest.TestCase):
    def test_a_wrong_body_is_caught(self):
        body = sub.price_payload("S1", "P1")
        body["data"]["type"] = "subscriptionPrice"
        self.assertTrue(_mod("asc_spec").validate_call(SPEC, "POST", "/v1/subscriptionPrices", body))

    def test_every_used_call_is_in_the_vendored_subset(self):
        for method, template in sub.USED_OPS:
            self.assertIn(method.lower(), SPEC["paths"].get(template, {}), (method, template))

    def test_builders_match_the_spec(self):
        v = _mod("asc_spec").validate_call
        d = sub.parse(MD)
        s = d["Monthly"]
        for method, path, body in [
            ("POST", "/v1/subscriptionGroups", sub.group_payload("APP", d)),
            ("POST", "/v1/subscriptions", sub.product_payload("G1", s, "n")),
            ("POST", "/v1/subscriptionPlanAvailabilities", sub.availability_payload("S1")),
            ("POST", "/v1/subscriptionPrices", sub.price_payload("S1", "P1")),
            ("PATCH", "/v1/apps/APP", {"data": {"type": "apps", "id": "APP", "attributes": sub.app_urls_attrs(d["Notifications"])}}),
        ]:
            self.assertEqual(v(SPEC, method, path, body), [], (method, path))


if __name__ == "__main__":
    unittest.main()
