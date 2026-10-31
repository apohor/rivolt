import XCTest
@testable import Rivolt

/// Against the server's own fixtures (backend's #1367 and #1369), so a drift in the plan fields or the 402 bodies fails the build.
final class ProPaywallTests: XCTestCase {
    private func d(_ s: String) -> Date { JSON.parseDate(s)! }
    private func me(_ name: String) throws -> AuthUser { try Fixture.decode(AuthUser.self, name) }

    func testBillingOffTheDemoAndAnAbsentPlanMeanNoPlanScreens() throws {
        XCTAssertEqual(PlanLogic.state(try me("me")), .off)
        XCTAssertEqual(PlanLogic.state(nil), .off)
        let noFields = try JSON.decoder().decode(AuthUser.self, from: Data(#"{"username": "a", "user_id": "u"}"#.utf8))
        XCTAssertEqual(PlanLogic.state(noFields), .off)
        let demo = try JSON.decoder().decode(AuthUser.self, from: Data(#"{"username": "a", "plan": "pro", "plan_source": "demo", "billing_enabled": true}"#.utf8))
        XCTAssertEqual(PlanLogic.state(demo), .off)
        let off = try JSON.decoder().decode(AuthUser.self, from: Data(#"{"username": "a", "plan": "pro", "plan_source": "apple", "billing_enabled": false}"#.utf8))
        XCTAssertEqual(PlanLogic.state(off), .off)
        XCTAssertEqual(SettingsLogic.groups(notificationsOffered: true).first?.rows, [.connection, .signOut])
    }

    func testTheRowIsListedOnlyWhereTheServerBills() {
        XCTAssertEqual(SettingsLogic.groups(notificationsOffered: true, planOffered: true).first?.rows, [.plan, .connection, .signOut])
        XCTAssertEqual(SettingsLogic.Row.plan.title, "Rivolt Pro")
    }

    func testTheStatesAreToldApartFromTheFixtures() throws {
        XCTAssertEqual(PlanLogic.state(try me("me_plan_free")), .free)
        XCTAssertEqual(PlanLogic.state(try me("me_plan_trial")), .trial(ends: d("2026-11-14T12:00:00Z")))
        XCTAssertEqual(PlanLogic.state(try me("me_plan_subscriber")), .pro(source: "stripe", periodEnd: d("2026-12-01T00:00:00Z")))
        XCTAssertEqual(PlanLogic.state(try me("me_plan_apple")), .pro(source: "apple", periodEnd: d("2026-12-01T00:00:00Z")))
        XCTAssertEqual(PlanLogic.state(try me("me_plan_founding")), .pro(source: "founding", periodEnd: d("2027-11-01T00:00:00Z")))
        XCTAssertEqual(try me("me_plan_free").historyDays, 90)
        XCTAssertNil(try me("me_plan_founding").historyDays)
    }

    func testALapseComesFromPlanEndedAt() throws {
        XCTAssertEqual(PlanLogic.state(try me("me_plan_lapsed")), .lapsed(endedOn: d("2026-10-01T00:00:00Z"), source: "stripe"))
    }

    func testTrialDaysLeft() {
        let now = d("2026-12-10T12:00:00Z")
        XCTAssertEqual(PlanLogic.trialLeft(d("2026-12-13T12:00:00Z"), now: now), "3 days left")
        XCTAssertEqual(PlanLogic.trialLeft(d("2026-12-11T12:00:00Z"), now: now), "1 day left")
        XCTAssertEqual(PlanLogic.trialLeft(d("2026-12-01T00:00:00Z"), now: now), "ends today")
        XCTAssertNil(PlanLogic.trialLeft(nil, now: now))
    }

    func testThePaywallStatesTheTrialThePriceAfterAndTheRenewalBeforeTheButton() {
        let t = ProCopy.terms
        XCTAssertTrue(t.contains("14 days free, then $34.99 a year (or $3.99 a month)"))
        XCTAssertTrue(t.contains("Renews automatically until cancelled"))
        XCTAssertTrue(t.contains("at least 24 hours before the end of the trial or any period"))
        XCTAssertTrue(t.contains("Settings → Apple ID → Subscriptions"))
        XCTAssertTrue(t.contains("charged to your Apple ID"))
        XCTAssertFalse(ProCopy.included.isEmpty)
        XCTAssertEqual(ProCopy.choiceLabel(true), "Subscribe: $34.99 a year, renews yearly")
        XCTAssertEqual(ProCopy.choiceLabel(false), "Subscribe: $3.99 a month, renews monthly")
    }

    func testTheCardCopyPerState() throws {
        let now = d("2026-11-11T12:00:00Z")
        XCTAssertEqual(ProCopy.card(.free)?.title, "Free")
        let trial = try XCTUnwrap(ProCopy.card(PlanLogic.state(try me("me_plan_trial")), now: now))
        XCTAssertTrue(trial.title.hasPrefix("Pro trial, ends"))
        XCTAssertTrue(trial.line.hasPrefix("3 days left. No card, nothing is charged."))
        XCTAssertEqual(ProCopy.card(PlanLogic.state(try me("me_plan_apple")))?.line, "Billed through Apple.")
        XCTAssertEqual(ProCopy.card(PlanLogic.state(try me("me_plan_subscriber")))?.line, "Billed through Stripe.")
        XCTAssertTrue(ProCopy.card(PlanLogic.state(try me("me_plan_founding")))?.title.hasPrefix("Rivolt Pro, free until") == true)
        let lapse = try XCTUnwrap(ProCopy.card(PlanLogic.state(try me("me_plan_lapsed"))))
        XCTAssertTrue(lapse.title.hasPrefix("Your Pro ended on"))
        XCTAssertTrue(lapse.line.contains("Nothing is deleted"))
        XCTAssertTrue(lapse.line.contains("in your export any time (Settings → Data)"))
        XCTAssertNil(ProCopy.card(.off))
        XCTAssertNil(ProCopy.trialLine(.free))
        XCTAssertTrue(ProCopy.trialLine(.lapsed(endedOn: nil, source: "trial"))?.contains("14 days of Pro were free") == true)
    }

    func testManageSubscriptionOpensTheAppStoresOwnPage() {
        XCTAssertEqual(PlanLogic.manageSubscriptionsURL.host, "apps.apple.com")
        XCTAssertEqual(ProCopy.restore, "Restore purchases")
        XCTAssertEqual(ProCopy.manage, "Manage subscription")
    }

    func testAccountDeletionTellsAnApplePayerToCancelFirst() {
        XCTAssertTrue(ProCopy.deletionNote.contains("cancel in Settings"))
        XCTAssertTrue(ProCopy.deletionNote.contains("doesn't cancel it"))
    }

    func testALimitIsReadByItsCodeAnd403IsNotOne() throws {
        let outside = try JSONSerialization.jsonObject(with: Fixture.data("err_outside_free_window")) as? [String: Any]
        let required = try JSONSerialization.jsonObject(with: Fixture.data("err_pro_required")) as? [String: Any]
        XCTAssertEqual(ProLimit.of(APIError.server(status: 402, message: outside?["error"] as? String ?? "")), .outsideFreeWindow)
        XCTAssertEqual(ProLimit.of(APIError.server(status: 402, message: required?["error"] as? String ?? "")), .proRequired)
        XCTAssertEqual(outside?["free_days"] as? Int, 90)
        XCTAssertNil(ProLimit.of(APIError.server(status: 403, message: "outside_free_window")))
        XCTAssertNil(ProLimit.of(APIError.server(status: 402, message: "something_else")))
        XCTAssertNil(ProLimit.of(APIError.transport("x")))
        XCTAssertTrue(ProLimit.outsideFreeWindow.text().contains("last 90 days"))
        XCTAssertTrue(ProLimit.proRequired.text().contains("kept either way"))
    }

    func testNothingCanBeBoughtYetAndTheScreenSaysSo() async {
        do {
            try await UnavailablePurchasing().purchase(yearly: true)
            XCTFail("purchase must not succeed before StoreKit exists")
        } catch {}
        do {
            try await UnavailablePurchasing().restore()
            XCTFail("restore must not succeed before StoreKit exists")
        } catch {}
        XCTAssertTrue(ProCopy.unavailable.contains("Nothing was charged"))
    }
}
