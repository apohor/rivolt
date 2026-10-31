import XCTest
@testable import Rivolt

final class ProPaywallTests: XCTestCase {
    private let now = JSON.parseDate("2026-12-10T12:00:00Z")!
    private func d(_ s: String) -> Date { JSON.parseDate(s)! }

    private func me(_ json: String) throws -> AuthUser {
        try JSON.decoder().decode(AuthUser.self, from: Data(json.utf8))
    }

    func testNoPlanOnTheAccountMeansNoPlanScreens() throws {
        XCTAssertEqual(PlanLogic.state(try me(#"{"username": "a", "user_id": "u"}"#), now: now), .off)
        XCTAssertEqual(PlanLogic.state(nil, now: now), .off)
        XCTAssertEqual(SettingsLogic.groups(notificationsOffered: true).first?.rows, [.connection, .signOut])
    }

    func testTheRowIsListedOnlyWhereTheServerBills() {
        XCTAssertEqual(SettingsLogic.groups(notificationsOffered: true, planOffered: true).first?.rows, [.plan, .connection, .signOut])
        XCTAssertEqual(SettingsLogic.Row.plan.title, "Rivolt Pro")
    }

    func testTheStatesAreToldApart() throws {
        XCTAssertEqual(PlanLogic.state(try me(#"{"username":"a","plan":"free"}"#), now: now), .free)
        XCTAssertEqual(PlanLogic.state(try me(#"{"username":"a","plan":"pro","source":"trial","trial_ends":"2026-12-20T00:00:00Z"}"#), now: now), .trial(ends: d("2026-12-20T00:00:00Z")))
        XCTAssertEqual(PlanLogic.state(try me(#"{"username":"a","plan":"pro","source":"apple","period_end":"2027-01-05T00:00:00Z"}"#), now: now), .pro(source: "apple", periodEnd: d("2027-01-05T00:00:00Z")))
        XCTAssertEqual(PlanLogic.state(try me(#"{"username":"a","plan":"pro","source":"mystery"}"#), now: now), .pro(source: "grant", periodEnd: nil))
    }

    func testALapseIsAFreeAccountWhoseTrialOrPeriodEnded() throws {
        XCTAssertEqual(PlanLogic.state(try me(#"{"username":"a","plan":"free","period_end":"2027-03-01T00:00:00Z"}"#), now: now), .free)
        XCTAssertEqual(PlanLogic.state(try me(#"{"username":"a","plan":"free","period_end":"2026-12-01T00:00:00Z"}"#), now: now), .lapsed(endedOn: d("2026-12-01T00:00:00Z")))
        XCTAssertEqual(PlanLogic.state(try me(#"{"username":"a","plan":"free","trial_ends":"2026-11-20T00:00:00Z","period_end":"2026-12-02T00:00:00Z"}"#), now: now), .lapsed(endedOn: d("2026-12-02T00:00:00Z")))
    }

    func testTrialDaysLeft() {
        XCTAssertEqual(PlanLogic.trialLeft(d("2026-12-13T12:00:00Z"), now: now), "3 days left")
        XCTAssertEqual(PlanLogic.trialLeft(d("2026-12-11T12:00:00Z"), now: now), "1 day left")
        XCTAssertEqual(PlanLogic.trialLeft(d("2026-12-01T00:00:00Z"), now: now), "ends today")
        XCTAssertNil(PlanLogic.trialLeft(nil, now: now))
    }

    func testThePaywallStatesTheTrialThePriceAfterAndTheRenewalBeforeTheButton() {
        let y = ProCopy.terms(yearly: true)
        XCTAssertTrue(y.contains("Free for 14 days, then $34.99 per year"))
        XCTAssertTrue(y.contains("renews automatically"))
        XCTAssertTrue(y.contains("at least 24 hours before the end of the current period"))
        XCTAssertTrue(y.contains("Subscriptions"))
        XCTAssertTrue(ProCopy.terms(yearly: false).contains("$3.99 per month"))
        XCTAssertFalse(ProCopy.included.isEmpty)
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
