import Foundation

/// Which vehicle a screen opens on. A free account with several vehicles shows its primary; the rest are `proLocked` (listed, recording and
/// alerting, in the export, but their history is Pro). A locked vehicle is never the default, and is opened only when the person picks it
/// in this visit.
enum VehicleChoice {
    /// The id to show: the chosen one when it is open (or a locked one picked in this visit), else the first open one, else the first.
    static func id(_ vehicles: [Vehicle], chosen: String, pickedLocked: Bool = false) -> String? {
        guard !vehicles.isEmpty else { return nil }
        if let v = vehicles.first(where: { $0.id == chosen }), v.proLocked != true || pickedLocked { return v.id }
        return (vehicles.first { $0.proLocked != true } ?? vehicles[0]).id
    }

    static func selected(_ vehicles: [Vehicle], chosen: String, pickedLocked: Bool = false) -> Vehicle? {
        id(vehicles, chosen: chosen, pickedLocked: pickedLocked).flatMap { id in vehicles.first { $0.id == id } }
    }

    /// "Name · Pro" for a locked vehicle in a picker.
    static func label(_ v: Vehicle) -> String { v.proLocked == true ? "\(v.name) · Pro" : v.name }
}
