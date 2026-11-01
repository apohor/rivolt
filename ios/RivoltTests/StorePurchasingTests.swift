import XCTest
@testable import Rivolt

private final class Log: @unchecked Sendable {
    var reported: [String] = []
    var finished: [String] = []
    var tokens: [UUID] = []
    var changes = 0
}

private struct FakeGateway: StoreGateway {
    let log: Log
    var catalog = [StoreProduct(id: "p.month", period: .month), StoreProduct(id: "p.year", period: .year), StoreProduct(id: "p.week", period: .other)]
    var outcome: (StoreProduct) -> StoreOutcome = { _ in .cancelled }
    var restorableSigned: [String] = []

    func products(ids: [String]) async throws -> [StoreProduct] { catalog.filter { ids.contains($0.id) } }
    func purchase(_ product: StoreProduct, appAccountToken: UUID) async throws -> StoreOutcome {
        log.tokens.append(appAccountToken)
        return outcome(product)
    }
    func restorable() async throws -> [StoreTransaction] {
        restorableSigned.map { s in StoreTransaction(signed: s, finish: { [log] in log.finished.append(s) }) }
    }
}

final class StorePurchasingTests: XCTestCase {
    private let user = UUID(uuidString: "00000000-0000-4000-8000-000000000001")!

    private func make(_ log: Log, gateway: FakeGateway, serverAccepts: Bool = true) -> StorePurchasing {
        struct Refused: Error {}
        return StorePurchasing(gateway: gateway, productIDs: ["p.month", "p.year", "p.week"], accountToken: user,
                               report: { [log] s in
                                   if !serverAccepts { throw Refused() }
                                   log.reported.append(s)
                               },
                               onChange: { [log] in log.changes += 1 })
    }

    private func purchased(_ log: Log) -> (StoreProduct) -> StoreOutcome {
        { p in .purchased(StoreTransaction(signed: "jws-\(p.id)", finish: { [log] in log.finished.append("jws-\(p.id)") })) }
    }

    func testTheYearlyAndMonthlyProductsAreToldApartByPeriod() {
        let all = [StoreProduct(id: "a", period: .month), StoreProduct(id: "b", period: .year), StoreProduct(id: "c", period: .other)]
        XCTAssertEqual(StoreLogic.pick(all, yearly: true)?.id, "b")
        XCTAssertEqual(StoreLogic.pick(all, yearly: false)?.id, "a")
        XCTAssertNil(StoreLogic.pick([StoreProduct(id: "c", period: .other)], yearly: true))
    }

    func testTheAccountIdIsTheAppAccountToken() {
        XCTAssertEqual(StoreLogic.accountToken("00000000-0000-4000-8000-000000000001"), user)
        XCTAssertEqual(StoreLogic.accountToken("00000000-0000-4000-8000-00000000ABCD"), UUID(uuidString: "00000000-0000-4000-8000-00000000abcd"))
        XCTAssertNil(StoreLogic.accountToken("not-a-uuid"))
        XCTAssertNil(StoreLogic.accountToken(nil))
    }

    func testAYearlyPurchaseBuysTheYearlyProductWithTheAccountTokenThenReportsThenFinishes() async throws {
        let log = Log()
        var g = FakeGateway(log: log)
        g.outcome = purchased(log)
        try await make(log, gateway: g).purchase(yearly: true)
        XCTAssertEqual(log.tokens, [user])
        XCTAssertEqual(log.reported, ["jws-p.year"])
        XCTAssertEqual(log.finished, ["jws-p.year"])
        XCTAssertEqual(log.changes, 1)
    }

    func testAMonthlyPurchaseBuysTheMonthlyProduct() async throws {
        let log = Log()
        var g = FakeGateway(log: log)
        g.outcome = purchased(log)
        try await make(log, gateway: g).purchase(yearly: false)
        XCTAssertEqual(log.reported, ["jws-p.month"])
    }

    func testATransactionTheServerRefusesIsNotFinishedSoItComesAgain() async {
        let log = Log()
        var g = FakeGateway(log: log)
        g.outcome = purchased(log)
        do {
            try await make(log, gateway: g, serverAccepts: false).purchase(yearly: true)
            XCTFail("a refused transaction must not read as a success")
        } catch {
            XCTAssertEqual(error as? StoreError, .notConfirmed)
        }
        XCTAssertTrue(log.finished.isEmpty)
        XCTAssertEqual(log.changes, 0)
    }

    func testCancelAndPendingAreNotFailures() async {
        let log = Log()
        var g = FakeGateway(log: log)
        for (outcome, expected) in [(StoreOutcome.cancelled, StoreError.cancelled), (.pending, .pending)] {
            g.outcome = { _ in outcome }
            do { try await make(log, gateway: g).purchase(yearly: true); XCTFail("expected \(expected)") } catch { XCTAssertEqual(error as? StoreError, expected) }
        }
        XCTAssertTrue(log.reported.isEmpty)
        XCTAssertNil(ProCopy.purchaseMessage(StoreError.cancelled), "a cancel says nothing")
        XCTAssertEqual(ProCopy.purchaseMessage(StoreError.pending), ProCopy.pending)
    }

    func testNoProductOfThatPeriodIsAMissingProduct() async {
        let log = Log()
        var g = FakeGateway(log: log)
        g.catalog = [StoreProduct(id: "p.week", period: .other)]
        do { try await make(log, gateway: g).purchase(yearly: true); XCTFail("expected productMissing") } catch { XCTAssertEqual(error as? StoreError, .productMissing) }
    }

    func testRestoreReportsEveryEntitlementThenFinishesEach() async throws {
        let log = Log()
        var g = FakeGateway(log: log)
        g.restorableSigned = ["one", "two"]
        try await make(log, gateway: g).restore()
        XCTAssertEqual(log.reported, ["one", "two"])
        XCTAssertEqual(log.finished, ["one", "two"])
    }

    func testRestoreWithNothingSaysSo() async {
        let log = Log()
        do { try await make(log, gateway: FakeGateway(log: log)).restore(); XCTFail("expected nothingToRestore") } catch { XCTAssertEqual(error as? StoreError, .nothingToRestore) }
        XCTAssertEqual(ProCopy.purchaseMessage(StoreError.nothingToRestore), ProCopy.nothingToRestore)
    }

    func testTheServerSendsItsProductsAndEnvironment() throws {
        let d = JSON.decoder()
        let c = try d.decode(AppConfig.self, from: try Fixture.data("app_config"))
        XCTAssertEqual(c.iap?.environment, "Sandbox")
        XCTAssertEqual(c.iap?.products.count, 2)
        XCTAssertTrue(c.purchasesAvailable)
        let none = try d.decode(AppConfig.self, from: Data(#"{"server":"rivolt","api_version":1,"app_login":true}"#.utf8))
        XCTAssertNil(none.iap)
        XCTAssertFalse(none.purchasesAvailable, "no iap, no buy button")
        let empty = try d.decode(AppConfig.self, from: Data(#"{"server":"rivolt","api_version":1,"app_login":true,"iap":{"environment":"Sandbox","products":[]}}"#.utf8))
        XCTAssertFalse(empty.purchasesAvailable)
    }

    func testAPurchaseIsOfferedOnlyWhereTheServerBillsAndHasStoreKit() {
        XCTAssertTrue(StoreLogic.mayOffer(billingEnabled: true, iapAvailable: true))
        XCTAssertFalse(StoreLogic.mayOffer(billingEnabled: false, iapAvailable: true), "billing off: never sell what everyone has free")
        XCTAssertFalse(StoreLogic.mayOffer(billingEnabled: nil, iapAvailable: true))
        XCTAssertFalse(StoreLogic.mayOffer(billingEnabled: true, iapAvailable: false))
    }

    func testRestoreIsListedWhereBillingIsOffButStoreKitIsSetUp() {
        func account(_ restore: Bool) -> [SettingsLogic.Row] {
            SettingsLogic.groups(notificationsOffered: false, restoreOffered: restore).first { $0.title == "Account" }!.rows
        }
        XCTAssertTrue(account(true).contains(.restore))
        XCTAssertFalse(account(false).contains(.restore))
        XCTAssertEqual(SettingsLogic.Row.restore.title, "Restore purchases")
    }

    func testTheTransactionBodyKeepsItsCamelCaseKey() throws {
        let data = try JSONSerialization.data(withJSONObject: ["signedTransaction": "x"])
        let json = try XCTUnwrap(try JSONSerialization.jsonObject(with: data) as? [String: String])
        XCTAssertEqual(json["signedTransaction"], "x")
        XCTAssertNil(json["signed_transaction"])
    }

    func testTheMessagesNeverClaimAChargeWasTaken() {
        XCTAssertTrue(ProCopy.unavailable.contains("Nothing was charged"))
        XCTAssertFalse(ProCopy.notConfirmed.contains("Nothing was charged"), "a purchase may have gone through: do not say nothing was charged")
        XCTAssertTrue(ProCopy.notConfirmed.contains("Restore purchases"))
    }
}
