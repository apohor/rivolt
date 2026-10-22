import Foundation

/// The part of `GET /api/ledger/monthly` the Now page's "Charging cost" tile uses: what the month's charging came to,
/// the same number Money shows. Everything else in the ledger (receipts, energy, data quality) is ignored here.
struct LedgerMonthSpend: Decodable, Equatable {
    struct Spend: Decodable, Equatable {
        let total: Double
        /// Newer servers split the total into billed and estimated; the web adds the two when both are there.
        let billedTotal: Double?
        let estimatedTotal: Double?
    }

    let currency: String
    let spend: Spend

    /// The web's `statementTotal`: billed plus estimated when the server gives both, else the total.
    var statementTotal: Double {
        if let b = spend.billedTotal, let e = spend.estimatedTotal { return b + e }
        return spend.total
    }
}
