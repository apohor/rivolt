import Foundation

/// The App Store purchase (Pro 6): StoreKit 2, behind a small gateway so the rules are testable. The flow: load the server's products, tell
/// monthly from yearly by period, buy with `appAccountToken` = the account's id (the server refuses a transaction without it), report the
/// signed transaction to the server, and only then finish it with StoreKit. A transaction the server did not accept stays unfinished, so
/// StoreKit offers it again (the updates listener and Restore purchases both report it).

enum StorePeriod: Equatable { case month, year, other }

struct StoreProduct: Equatable {
    let id: String
    let period: StorePeriod
}

/// What a purchase or a restored entitlement hands back: the signed transaction, and how to finish it.
struct StoreTransaction {
    let signed: String
    let finish: @Sendable () async -> Void
}

enum StoreOutcome {
    case purchased(StoreTransaction)
    case cancelled
    /// Ask to Buy or a payment that needs another step: the purchase arrives later through the updates listener.
    case pending
}

protocol StoreGateway {
    func products(ids: [String]) async throws -> [StoreProduct]
    func purchase(_ product: StoreProduct, appAccountToken: UUID) async throws -> StoreOutcome
    /// The current entitlements, signed (after the person asks to restore).
    func restorable() async throws -> [StoreTransaction]
}

enum StoreError: Error, Equatable {
    case notConfigured
    case productMissing
    case cancelled
    case pending
    case nothingToRestore
    /// The server did not accept the transaction. It is not finished, so it is offered again.
    case notConfirmed
}

enum StoreLogic {
    /// The server's product list names no labels: the yearly one is the product whose period is a year, and the monthly one a month.
    static func pick(_ products: [StoreProduct], yearly: Bool) -> StoreProduct? {
        products.first { $0.period == (yearly ? .year : .month) }
    }

    /// The account id as StoreKit's `appAccountToken` (the server parses it case-insensitively); nil for anything that is not a UUID.
    static func accountToken(_ userID: String?) -> UUID? { userID.flatMap(UUID.init(uuidString:)) }
}

struct StorePurchasing: ProPurchasing {
    let gateway: StoreGateway
    let productIDs: [String]
    let accountToken: UUID
    /// Reports a signed transaction to the server; throws when it is not accepted.
    let report: (String) async throws -> Void
    /// Called after the server accepted something, so the screens read the plan again.
    var onChange: () async -> Void = {}

    func purchase(yearly: Bool) async throws {
        guard !productIDs.isEmpty else { throw StoreError.notConfigured }
        guard let product = StoreLogic.pick(try await gateway.products(ids: productIDs), yearly: yearly) else { throw StoreError.productMissing }
        switch try await gateway.purchase(product, appAccountToken: accountToken) {
        case .cancelled: throw StoreError.cancelled
        case .pending: throw StoreError.pending
        case .purchased(let t): try await confirm(t)
        }
    }

    func restore() async throws {
        let found = try await gateway.restorable()
        guard !found.isEmpty else { throw StoreError.nothingToRestore }
        var failed = false
        for t in found {
            do { try await confirm(t) } catch { failed = true }
        }
        if failed { throw StoreError.notConfirmed }
    }

    /// A transaction that arrived on its own (the updates listener).
    func confirmUpdate(_ t: StoreTransaction) async throws { try await confirm(t) }

    /// Server first, then finish: a transaction is finished only once the server has it.
    private func confirm(_ t: StoreTransaction) async throws {
        do { try await report(t.signed) } catch { throw StoreError.notConfirmed }
        await t.finish()
        await onChange()
    }
}
