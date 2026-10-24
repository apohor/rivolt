import Foundation

// The Money tab's reads, per the ledger contract v1 (with its additive §9-§11 changes). The server owns every money and
// energy total: the phone shows what it gets and never re-sums receipts into totals or splits. Fields a newer server
// added are optional, so an older build still decodes. `km_from_home` is shown rounded and never stored.

/// Sessions, energy and money of one bucket (a kind, a place, or a place and kind).
struct LedgerTotals: Decodable, Equatable {
    let sessions: Int
    let kwh: Double
    let cost: Double
}

struct LedgerByKind: Decodable, Equatable {
    let ac: LedgerTotals
    let dcfc: LedgerTotals
}

struct LedgerByPlace: Decodable, Equatable {
    let home: LedgerTotals
    let away: LedgerTotals
    let unknown: LedgerTotals
}

/// §11: every place crossed with every kind, all six cells when present.
struct LedgerByPlaceKind: Decodable, Equatable {
    let home: LedgerByKind
    let away: LedgerByKind
    let unknown: LedgerByKind
}

/// One charging session folded into a receipt.
struct LedgerSession: Decodable, Equatable {
    let chargeId: String
    let startedAt: Date
    let kwh: Double?
    let cost: Double?
    let costSource: String
    let kind: String?
    let network: String?
    let networkState: String?
    let billedCost: Double?
    let billedCurrency: String?
    let needsRate: Bool?
    let rateSlug: String?
    let flags: [String]
    /// The energy was lost and worked out from the battery's change in charge level: this session's kWh and cost are estimates.
    var energyEstimated: Bool? = nil
}

/// One plug-in: its sessions (top-ups and fragments folded in), what it cost and where the price came from.
struct LedgerReceipt: Decodable, Equatable, Identifiable {
    let receiptId: String
    let kind: String
    let place: String
    /// Display only, rounded when shown; never kept.
    let kmFromHome: Double?
    let startedAt: Date
    let localDate: String
    let spansDays: Int
    let socStart: Double?
    let socEnd: Double?
    let kwh: Double
    let peakKw: Double?
    let cost: Double?
    let costSource: String
    let rate: Double?
    /// True when any session of this plug-in has an estimated energy figure.
    var energyEstimated: Bool? = nil
    let network: String?
    let networkState: String?
    let billedCost: Double?
    let billedCurrency: String?
    let billedVendor: String?
    let billedCharger: String?
    let estimateMonth: String?
    let needsRate: Bool?
    let rateSlug: String?
    let billedSessions: Int?
    let estimatedSessions: Int?
    let sessionCount: Int
    let unpricedSessions: Int
    let flags: [String]
    let sessions: [LedgerSession]

    var id: String { receiptId }
}

/// One month of the ledger. The summary's months come without receipts.
struct LedgerMonth: Decodable, Equatable {
    struct Range: Decodable, Equatable {
        let start: Date
        let end: Date
    }

    struct BySource: Decodable, Equatable {
        let rivian: Double?
        let homeRate: Double?
        let networkEstimate: Double?
        let user: Double?
    }

    struct Spend: Decodable, Equatable {
        let total: Double
        let bySource: BySource?
        let estimatedShare: Double?
        let pricedSessions: Int
        let unpricedSessions: Int
        let unpricedKwh: Double
        let unpricedHomeRateEstimate: Double?
        let estimatePendingSessions: Int?
        let estimatePendingKwh: Double?
        let observedEstimateCost: Double?
        let observedEstimateSessions: Int?
        let unpricedFastSessions: Int?
        let billedTotal: Double?
        let estimatedTotal: Double?
    }

    struct Energy: Decodable, Equatable {
        let sessions: Int
        let kwhAdded: Double
        let nullEnergySessions: Int
        /// True when the month's `kwhAdded` includes a charge whose energy was estimated.
        var includesEstimated: Bool? = nil
        let byKind: LedgerByKind
        let byPlace: LedgerByPlace?
        let byPlaceKind: LedgerByPlaceKind?
    }

    struct Driving: Decodable, Equatable {
        let drives: Int
        let miles: Double
        let milesWithEnergy: Double?
        let kwhUsedKnown: Double?
        let coverage: Double?
        let costPerMile: Double?
    }

    struct DataQuality: Decodable, Equatable {
        let costSourceInferred: Bool
        let costSourceDerivedSessions: Int?
        let supersededExcluded: Int
        let drivesSupersededExcluded: Int
        let openSessions: Int
        let currencyMismatchExcluded: Int
    }

    /// "2026-10".
    let period: String
    let range: Range
    let currency: String
    let isCurrentMonth: Bool
    let firstActivityAt: Date?
    let spend: Spend
    let billedSessions: Int?
    let estimatedSessions: Int?
    let energy: Energy
    let driving: Driving
    let dataQuality: DataQuality
    let receipts: [LedgerReceipt]?
}

/// `GET /api/ledger/summary`: the months (oldest first) and, with DCFC auto-network on, how many charges wait for a look.
struct LedgerSummary: Decodable, Equatable {
    let currency: String
    let months: [LedgerMonth]
    let toConfirmCount: Int?
}

/// `GET /api/ledger/savings`: what moving home charging into the off-peak windows could have saved. Always an estimate;
/// `needs` is set instead of a figure when something is missing.
struct LedgerSavings: Decodable, Equatable {
    let currency: String?
    let couldHaveSaved: Double?
    let sessionsMoved: Int?
    let sessionsLeftOut: Int?
    /// Counts by reason. The decoder's key strategy turns the reason keys into camel case ("pluggedInTooBriefly").
    let leftOutReasons: [String: Int]?
    let sessionsConsidered: Int?
    let asChargedTotal: Double?
    let ifMovedTotal: Double?
    let needs: String?
    let basis: String?
    /// Share of home charging by energy that ran in off-peak hours (0 to 100); absent without a schedule, a home rate or
    /// enough sessions. The share card's optional line.
    let offpeakSharePct: Double?
    /// A user on a flat rate gets only `flatRate` and `reason` ("flat_rate") and the sentence: no estimate field of any kind,
    /// so the card says so and the share card has no off-peak line to offer.
    let flatRate: Bool?
    let reason: String?
}

/// A charge whose auto-detected network waits for the owner's look (the "To confirm" list, all months), from
/// `GET /api/charges/to-confirm`, which has no coordinates; nor has this type a field for them. Kept in memory only
/// (never in the App Group or on disk).
struct ConfirmCharge: Decodable, Equatable, Identifiable {
    struct Evidence: Decodable, Equatable {
        struct Candidate: Decodable, Equatable { let network: String? }
        let candidates: [Candidate]?
    }

    let id: String
    let startedAt: Date
    let pricePerKwh: Double
    let cost: Double
    let currency: String?
    let networkState: String?
    let networkEvidence: Evidence?

    private enum CodingKeys: String, CodingKey {
        case id = "ID", startedAt = "StartedAt", pricePerKwh = "PricePerKWh", cost = "Cost", currency = "Currency"
        case networkState, networkEvidence
    }
}

/// The one field of the charging settings Money reads: whether a home rate is set (the empty month's wording).
struct ChargingRate: Decodable, Equatable {
    let homePricePerKwh: Double?
}
