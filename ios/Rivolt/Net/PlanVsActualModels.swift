import Foundation

/// `GET /api/trips/saved/{id}/actual` (plan-vs-actual contract v1): a saved trip against the outing that was driven. A null is
/// unknown, never zero; an incomplete cost has `actual == nil` and says how much is known. Every field is optional here, so an
/// answer from a newer server still reads.
struct PlanVsActual: Decodable, Equatable {
    struct Link: Decodable, Equatable {
        var tripId: String?
        var confidence: Double?
        var band: String?
        var linkedAt: LenientDate?
        var linkStale: Bool?
    }

    struct Candidate: Decodable, Equatable {
        var tripId: String?
        var startedAt: LenientDate?
        var endedAt: LenientDate?
        var confidence: Double?
        var band: String?
        var miles: Double?
        var inProgress: Bool?
    }

    struct Reason: Decodable, Equatable {
        var code: String?
        var amount: Double?
        var detail: String?
    }

    struct Metric: Decodable, Equatable {
        var metric: String?
        var unit: String?
        var planned: Double?
        var actual: Double?
        var delta: Double?
        var actualComplete: Bool?
        var actualKnown: Double?
        var unpricedSessions: Int?
        var actualSources: [String: Double]?
        var reasons: [Reason]?
    }

    struct Leg: Decodable, Equatable {
        var startedAt: LenientDate?
        var arrivedAt: LenientDate?
        var miles: Double?
    }

    struct Comparison: Decodable, Equatable {
        /// "in_progress" until the outing ends, then "complete".
        var status: String?
        var currency: String?
        var leg: Leg?
        var metrics: [Metric]?
    }

    var savedTripId: String?
    /// "linked", "suggested" or "none".
    var status: String?
    var link: Link?
    var candidates: [Candidate]?
    var comparison: Comparison?
    /// 1-based, on the days of a multi-day answer.
    var day: Int?
}

/// One comparison for a one-day trip, or one per day for a multi-day one.
struct PlanVsActualAnswer: Decodable, Equatable {
    var parts: [PlanVsActual]

    private struct Multi: Decodable { var kind: String?; var days: [PlanVsActual]? }

    init(parts: [PlanVsActual]) { self.parts = parts }

    init(from decoder: Decoder) throws {
        if let m = try? Multi(from: decoder), m.kind == "multiday" {
            parts = m.days ?? []
        } else {
            parts = [try PlanVsActual(from: decoder)]
        }
    }
}

/// `POST /api/trips/saved/{id}/link` and `/unlink`.
struct PlanVsActualLinkBody: Encodable, Equatable {
    var tripId: String
    var day: Int?
}
