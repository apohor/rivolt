import Foundation

/// `GET /api/live-session/{id}`: the charge in progress, as Rivian reports it plus our own estimate of its cost. Every
/// field is optional on the wire (a home charge often comes back empty), so decoding fills in zeros and empty text and
/// the card decides what is worth showing. Distances are kilometres, like the rest of the API.
struct LiveSession: Decodable, Equatable {
    let active: Bool
    let timeElapsedSeconds: Double
    let timeRemainingSeconds: Double
    let powerKw: Double
    let kilometersChargedPerHour: Double
    let rangeAddedKm: Double
    let totalChargedEnergyKwh: Double
    let socPct: Double
    /// The cost so far as Rivian bills it, a number as text; empty when Rivian reports none (home charging).
    let currentPrice: String
    let currentCurrency: String
    let isFreeSession: Bool
    let isRivianCharger: Bool
    /// Our estimate from the home rate, only when Rivian reported no price.
    let estimatedCost: Double?
    let estimatedCurrency: String?

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        active = try c.decodeIfPresent(Bool.self, forKey: .active) ?? false
        timeElapsedSeconds = try c.decodeIfPresent(Double.self, forKey: .timeElapsedSeconds) ?? 0
        timeRemainingSeconds = try c.decodeIfPresent(Double.self, forKey: .timeRemainingSeconds) ?? 0
        powerKw = try c.decodeIfPresent(Double.self, forKey: .powerKw) ?? 0
        kilometersChargedPerHour = try c.decodeIfPresent(Double.self, forKey: .kilometersChargedPerHour) ?? 0
        rangeAddedKm = try c.decodeIfPresent(Double.self, forKey: .rangeAddedKm) ?? 0
        totalChargedEnergyKwh = try c.decodeIfPresent(Double.self, forKey: .totalChargedEnergyKwh) ?? 0
        socPct = try c.decodeIfPresent(Double.self, forKey: .socPct) ?? 0
        currentPrice = try c.decodeIfPresent(String.self, forKey: .currentPrice) ?? ""
        currentCurrency = try c.decodeIfPresent(String.self, forKey: .currentCurrency) ?? ""
        isFreeSession = try c.decodeIfPresent(Bool.self, forKey: .isFreeSession) ?? false
        isRivianCharger = try c.decodeIfPresent(Bool.self, forKey: .isRivianCharger) ?? false
        estimatedCost = try c.decodeIfPresent(Double.self, forKey: .estimatedCost)
        estimatedCurrency = try c.decodeIfPresent(String.self, forKey: .estimatedCurrency)
    }

    init(active: Bool = false, timeElapsedSeconds: Double = 0, timeRemainingSeconds: Double = 0, powerKw: Double = 0,
         kilometersChargedPerHour: Double = 0, rangeAddedKm: Double = 0, totalChargedEnergyKwh: Double = 0, socPct: Double = 0,
         currentPrice: String = "", currentCurrency: String = "", isFreeSession: Bool = false, isRivianCharger: Bool = false,
         estimatedCost: Double? = nil, estimatedCurrency: String? = nil) {
        self.active = active
        self.timeElapsedSeconds = timeElapsedSeconds
        self.timeRemainingSeconds = timeRemainingSeconds
        self.powerKw = powerKw
        self.kilometersChargedPerHour = kilometersChargedPerHour
        self.rangeAddedKm = rangeAddedKm
        self.totalChargedEnergyKwh = totalChargedEnergyKwh
        self.socPct = socPct
        self.currentPrice = currentPrice
        self.currentCurrency = currentCurrency
        self.isFreeSession = isFreeSession
        self.isRivianCharger = isRivianCharger
        self.estimatedCost = estimatedCost
        self.estimatedCurrency = estimatedCurrency
    }

    private enum CodingKeys: String, CodingKey {
        case active, timeElapsedSeconds, timeRemainingSeconds, powerKw, kilometersChargedPerHour, rangeAddedKm
        case totalChargedEnergyKwh, socPct, currentPrice, currentCurrency, isFreeSession, isRivianCharger
        case estimatedCost, estimatedCurrency
    }
}

/// `GET /api/live-drive/{id}`: the drive in progress (while the truck is in gear). Distances are miles on this wire.
struct LiveDrive: Decodable, Equatable {
    let elapsedSec: Double
    let distanceMi: Double
    let maxSpeedMph: Double
    let avgSpeedMph: Double
    let energyUsedKwh: Double
    let miPerKwh: Double

    init(elapsedSec: Double = 0, distanceMi: Double = 0, maxSpeedMph: Double = 0, avgSpeedMph: Double = 0, energyUsedKwh: Double = 0, miPerKwh: Double = 0) {
        self.elapsedSec = elapsedSec
        self.distanceMi = distanceMi
        self.maxSpeedMph = maxSpeedMph
        self.avgSpeedMph = avgSpeedMph
        self.energyUsedKwh = energyUsedKwh
        self.miPerKwh = miPerKwh
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        elapsedSec = try c.decodeIfPresent(Double.self, forKey: .elapsedSec) ?? 0
        distanceMi = try c.decodeIfPresent(Double.self, forKey: .distanceMi) ?? 0
        maxSpeedMph = try c.decodeIfPresent(Double.self, forKey: .maxSpeedMph) ?? 0
        avgSpeedMph = try c.decodeIfPresent(Double.self, forKey: .avgSpeedMph) ?? 0
        energyUsedKwh = try c.decodeIfPresent(Double.self, forKey: .energyUsedKwh) ?? 0
        miPerKwh = try c.decodeIfPresent(Double.self, forKey: .miPerKwh) ?? 0
    }

    private enum CodingKeys: String, CodingKey {
        case elapsedSec, distanceMi, maxSpeedMph, avgSpeedMph, energyUsedKwh, miPerKwh
    }
}

/// One row of `GET /api/charges` (newest first), only what the Now card says about the last or current charge: where
/// (Home, Away or Fast, never a coordinate) and what it cost. The list rows also carry a location; it is not decoded.
struct ChargeBrief: Decodable, Equatable {
    let id: String
    let startedAt: Date?
    let endedAt: Date?
    let energyAddedKwh: Double
    let kind: String?
    let place: String?
    let cost: Double
    let currency: String?
    let estimatedCost: Double?
    let estimatedCurrency: String?

    /// The same label the web's charge list uses.
    var placeLabel: String? {
        if kind == "dcfc" { return "Fast" }
        if kind == "ac" {
            if place == "home" { return "Home" }
            if place == "away" { return "Away" }
        }
        return nil
    }

    private enum CodingKeys: String, CodingKey {
        case id = "ID", startedAt = "StartedAt", endedAt = "EndedAt", energyAddedKwh = "EnergyAddedKWh"
        case kind, place, cost = "Cost", currency = "Currency", estimatedCost, estimatedCurrency
    }
}

/// One row of `GET /api/drives` (newest first): the last drive's figures. The route and coordinates are not decoded.
struct DriveBrief: Decodable, Equatable {
    let id: String
    let startedAt: Date?
    let endedAt: Date?
    let distanceMi: Double
    let avgSpeedMph: Double
    let energyUsedKwh: Double
    let estimatedCost: Double?
    let estimatedCurrency: String?

    /// Miles per kWh, when the drive has an energy figure.
    var miPerKwh: Double? { energyUsedKwh > 0 && distanceMi > 0 ? distanceMi / energyUsedKwh : nil }

    private enum CodingKeys: String, CodingKey {
        case id = "ID", startedAt = "StartedAt", endedAt = "EndedAt", distanceMi = "DistanceMi", avgSpeedMph = "AvgSpeedMph"
        case energyUsedKwh = "EnergyUsedKWh", estimatedCost, estimatedCurrency
    }
}
