import Foundation
import StoreKit

/// The real gateway: StoreKit 2. Everything here is a thin call; the rules are in `StorePurchasing`.
struct StoreKit2Gateway: StoreGateway {
    func products(ids: [String]) async throws -> [StoreProduct] {
        try await Product.products(for: ids).map { StoreProduct(id: $0.id, period: Self.period($0)) }
    }

    func purchase(_ product: StoreProduct, appAccountToken: UUID) async throws -> StoreOutcome {
        guard let real = try await Product.products(for: [product.id]).first else { throw StoreError.productMissing }
        switch try await real.purchase(options: [.appAccountToken(appAccountToken)]) {
        case .success(let verification):
            guard case .verified(let transaction) = verification else { throw StoreError.notConfirmed }
            return .purchased(StoreTransaction(signed: verification.jwsRepresentation, finish: { await transaction.finish() }))
        case .userCancelled: return .cancelled
        case .pending: return .pending
        @unknown default: return .pending
        }
    }

    func restorable() async throws -> [StoreTransaction] {
        try await AppStore.sync()
        var out: [StoreTransaction] = []
        for await entitlement in Transaction.currentEntitlements {
            guard case .verified(let transaction) = entitlement else { continue }
            out.append(StoreTransaction(signed: entitlement.jwsRepresentation, finish: { await transaction.finish() }))
        }
        return out
    }

    /// The transactions that arrive outside a purchase (a renewal, Ask to Buy approved, a purchase made on another device): report each to
    /// the server, then finish it. Runs for as long as the app is signed in.
    static func listen(purchasing: StorePurchasing) -> Task<Void, Never> {
        Task.detached {
            for await update in Transaction.updates {
                guard case .verified(let transaction) = update else { continue }
                let t = StoreTransaction(signed: update.jwsRepresentation, finish: { await transaction.finish() })
                // Confirm does server-then-finish; one that the server refuses stays unfinished and comes again.
                try? await purchasing.confirmUpdate(t)
            }
        }
    }

    private static func period(_ p: Product) -> StorePeriod {
        guard let s = p.subscription?.subscriptionPeriod, s.value == 1 else { return .other }
        switch s.unit {
        case .year: return .year
        case .month: return .month
        default: return .other
        }
    }
}
