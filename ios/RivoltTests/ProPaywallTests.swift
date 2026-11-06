import XCTest
@testable import Rivolt

/// Against the server's own fixtures (backend's #1367 and #1369), so a drift in the plan fields or the 402 bodies fails the build.
final class ProPaywallTests: XCTestCase {
    private func d(_ s: String) -> Date { JSON.parseDate(s)! }
    /// The calendar date the server names (B8), as the card holds it: local noon of that date, which formats to that date in any zone.
    private func cal(_ s: String) -> Date { PlanLogic.calendarDate(s)! }
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
        // No trial any more: an old trial row reads as Pro granted to the account.
        XCTAssertEqual(PlanLogic.state(try me("me_plan_trial")), .pro(source: "grant", periodEnd: cal("2026-11-14")))
        XCTAssertEqual(PlanLogic.state(try me("me_plan_subscriber")), .pro(source: "stripe", periodEnd: cal("2026-12-01")))
        XCTAssertEqual(PlanLogic.state(try me("me_plan_apple")), .pro(source: "apple", periodEnd: d("2026-12-01T00:00:00Z")))
        XCTAssertEqual(PlanLogic.state(try me("me_plan_founding")), .pro(source: "founding", periodEnd: cal("2027-11-01")))
        XCTAssertEqual(try me("me_plan_free").historyDays, 90)
        XCTAssertNil(try me("me_plan_founding").historyDays)
    }

    func testACancelledPlanAndAFailedPaymentAreTheirOwnStates() throws {
        XCTAssertEqual(PlanLogic.state(try me("me_plan_cancelled")), .cancelled(source: "stripe", until: cal("2026-12-01")))
        XCTAssertEqual(PlanLogic.state(try me("me_plan_grace")), .paymentFailed(source: "stripe", until: cal("2026-12-08")))
        // Healthy plans carry neither key as a value.
        XCTAssertEqual(PlanLogic.state(try me("me_plan_subscriber")), .pro(source: "stripe", periodEnd: cal("2026-12-01")))
        // A founding or granted plan is never "cancelled", whatever the field says.
        let founding = try JSON.decoder().decode(AuthUser.self, from: Data(#"{"username":"a","plan":"pro","plan_source":"founding","plan_cancels_at":"2027-01-01T00:00:00Z","billing_enabled":true}"#.utf8))
        XCTAssertEqual(PlanLogic.state(founding), .pro(source: "founding", periodEnd: nil))
        let cancelled = try XCTUnwrap(ProCopy.card(PlanLogic.state(try me("me_plan_cancelled"))))
        XCTAssertEqual(cancelled.title, "Rivolt Pro")
        XCTAssertTrue(cancelled.line.hasPrefix("Won't renew; ends "))
        XCTAssertTrue(cancelled.line.hasSuffix(". Nothing is deleted after."))
        XCTAssertFalse((cancelled.title + cancelled.line).contains("renews"))
        let failed = try XCTUnwrap(ProCopy.card(PlanLogic.state(try me("me_plan_grace"))))
        XCTAssertEqual(failed.title, "Rivolt Pro, payment failed")
        XCTAssertTrue(failed.line.hasPrefix("Update your card from the payment email we sent, by"))
        XCTAssertTrue(failed.line.hasSuffix("Nothing is deleted either way."))
    }

    func testALapseComesFromPlanEndedAt() throws {
        XCTAssertEqual(PlanLogic.state(try me("me_plan_lapsed")), .lapsed(endedOn: cal("2026-10-01"), source: "stripe"))
    }

    func testThePaywallStatesThePriceAndTheRenewalBeforeTheButtonAndPromisesNoTrial() {
        let t = ProCopy.terms
        XCTAssertTrue(t.hasPrefix("$34.99 a year (or $3.99 a month)."))
        XCTAssertFalse(t.lowercased().contains("trial"))
        XCTAssertFalse(t.contains("14 days"))
        XCTAssertTrue(t.contains("Renews automatically until cancelled"))
        XCTAssertTrue(t.contains("at least 24 hours before the end of any period"))
        XCTAssertTrue(t.contains("Settings → Apple ID → Subscriptions"))
        XCTAssertTrue(t.contains("charged to your Apple ID"))
        XCTAssertFalse(ProCopy.included.isEmpty)
        XCTAssertEqual(ProCopy.choiceLabel(true), "Subscribe: $34.99 a year, renews yearly")
        XCTAssertEqual(ProCopy.choiceLabel(false), "Subscribe: $3.99 a month, renews monthly")
    }

    func testTheCardCopyPerState() throws {
        let now = d("2026-11-11T12:00:00Z")
        XCTAssertEqual(ProCopy.card(.free)?.title, "Free")
        // An old trial row shows as plain Pro, never as a trial.
        let old = try XCTUnwrap(ProCopy.card(PlanLogic.state(try me("me_plan_trial")), now: now))
        XCTAssertEqual(old.title, "Rivolt Pro")
        XCTAssertFalse((old.title + old.line).lowercased().contains("trial"))
        XCTAssertEqual(ProCopy.card(PlanLogic.state(try me("me_plan_apple")))?.line, "Billed through Apple.")
        XCTAssertEqual(ProCopy.card(PlanLogic.state(try me("me_plan_subscriber")))?.line, "Billed through Stripe.")
        XCTAssertTrue(ProCopy.card(PlanLogic.state(try me("me_plan_founding")))?.title.hasPrefix("Rivolt Pro, free until") == true)
        let lapse = try XCTUnwrap(ProCopy.card(PlanLogic.state(try me("me_plan_lapsed"))))
        XCTAssertTrue(lapse.title.hasPrefix("Your Pro ended on"))
        XCTAssertTrue(lapse.line.contains("Nothing is deleted"))
        XCTAssertTrue(lapse.line.contains("in your export any time (Settings → Data)"))
        XCTAssertNil(ProCopy.card(.off))
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

    func testTheCSVOfAnOlderMonthIsProOnlyForAFreeOrLapsedAccount() {
        XCTAssertEqual(PlanLogic.monthsBack("2026-10", now: "2026-10"), 0)
        XCTAssertEqual(PlanLogic.monthsBack("2026-08", now: "2026-10"), 2)
        XCTAssertEqual(PlanLogic.monthsBack("2026-07", now: "2026-10"), 3)
        XCTAssertEqual(PlanLogic.monthsBack("2025-12", now: "2026-02"), 2)
        XCTAssertEqual(PlanLogic.monthsBack("junk", now: "2026-10"), 0)
        XCTAssertFalse(PlanLogic.csvIsPro(.free, month: "2026-08", now: "2026-10"))
        XCTAssertTrue(PlanLogic.csvIsPro(.free, month: "2026-07", now: "2026-10"))
        XCTAssertTrue(PlanLogic.csvIsPro(.lapsed(endedOn: nil, source: nil), month: "2026-01", now: "2026-10"))
        XCTAssertFalse(PlanLogic.csvIsPro(.off, month: "2020-01", now: "2026-10"))
        XCTAssertFalse(PlanLogic.csvIsPro(.pro(source: "apple", periodEnd: nil), month: "2020-01", now: "2026-10"))
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

    func testTheDemoLinksArePlainAndFitTheNote() {
        XCTAssertEqual(DemoLinks.all.map(\.url.absoluteString),
                       ["https://demo.rivolt.dev/demo/trips/plan", "https://demo.rivolt.dev/demo/activity", "https://demo.rivolt.dev/demo/truck"])
        for l in DemoLinks.all { XCTAssertNil(URL(string: l.url.absoluteString)?.query) }
        XCTAssertEqual(DemoLinks.forNote(limit: .proRequired, message: ProCopy.plannerLimit), [DemoLinks.planner])
        XCTAssertEqual(DemoLinks.forNote(limit: .proRequired, message: ProCopy.savedTripsLimit()), [DemoLinks.planner])
        XCTAssertEqual(DemoLinks.forNote(limit: .outsideFreeWindow, message: nil), [DemoLinks.history, DemoLinks.trends])
        XCTAssertTrue(DemoLinks.forNote(limit: .proRequired, message: ProCopy.vehicleLimit).isEmpty)
    }

    /// B8: 2026-12-01T00:00:00Z is Nov 30 in Pacific; Stripe's Portal says December 1, and so must the card, in any zone.
    func testTheCardShowsTheServersUTCDateNotTheInstantInTheDevicesZone() throws {
        let cancelled = try XCTUnwrap(ProCopy.card(PlanLogic.state(try me("me_plan_cancelled"))))
        XCTAssertTrue(cancelled.line.contains("December 1, 2026"), cancelled.line)
        XCTAssertFalse(cancelled.line.contains("November 30"), cancelled.line)
        // Apple's date is the device's own: the instant, formatted on the device.
        XCTAssertEqual(PlanLogic.state(try me("me_plan_apple")), .pro(source: "apple", periodEnd: d("2026-12-01T00:00:00Z")))
        // An older server that sends no dates keeps working from the instants.
        var old = try me("me_plan_subscriber")
        old.planDateBasis = nil
        old.planPeriodEndDate = nil
        XCTAssertEqual(PlanLogic.state(old), .pro(source: "stripe", periodEnd: d("2026-12-01T00:00:00Z")))
        XCTAssertEqual(PlanLogic.date(cal("2026-12-01")), "December 1, 2026")
    }
}
