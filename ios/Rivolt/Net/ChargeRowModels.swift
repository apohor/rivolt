import Foundation

/// One charge's row from `GET /api/charges/{id}`, read once on the charge's screen and shared by the map and the session
/// card. Ten fields and no more: where the charge happened, its peak power and its network (the charger map's), and the
/// six the session card adds (miles added, average power, how it ended, where the row came from, the thermal-management
/// energy and the time spent charging). The address, the receipt ids, the cost fields (the activity row has those) and the
/// rest of the row are not decoded. Held in memory while the charge's screen is open, never stored.
struct ChargeRow: Decodable, Equatable {
    let lat: Double
    let lon: Double
    let maxPowerKW: Double?
    let network: String?
    let milesAdded: Double?
    let avgPowerKw: Double?
    let finalState: String?
    let source: String?
    /// The energy the battery management system spent heating or cooling the pack; null when not reported.
    let thermalKwh: Double?
    /// Seconds spent above 0 kW; 0 or absent on rows that predate it.
    let activeSeconds: Double?

    private enum CodingKeys: String, CodingKey {
        case lat = "Lat", lon = "Lon", maxPowerKW = "MaxPowerKW", network = "Network"
        case milesAdded = "MilesAdded", avgPowerKw = "AvgPowerKW", finalState = "FinalState", source = "Source"
        case thermalKwh = "ThermalKWh", activeSeconds = "ActiveSeconds"
    }

    /// What the charger map needs, with the network it should use (the ledger's wins over the row's).
    func location(network override: String? = nil) -> ChargeLocation {
        ChargeLocation(lat: lat, lon: lon, maxPowerKW: maxPowerKW, network: override ?? network)
    }
}
