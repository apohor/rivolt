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

/// "Showing R1T. Pro includes all vehicles.": a free account with several vehicles gets answers narrowed to its primary, and the server says
/// so with `X-Rivolt-Vehicle-Scope: <vehicle id>` on each narrowed answer (absent otherwise: Pro, one vehicle, billing off). The last answer
/// from a limited route decides.
@MainActor @Observable
final class VehicleScope {
    static let shared = VehicleScope()
    private(set) var id: String?

    /// The routes behind the limit; the header is on these and only these.
    nonisolated static let routes = ["/api/ledger/monthly", "/api/summary", "/api/trips", "/api/savings", "/api/activity", "/api/charge-split",
                                     "/api/charges", "/api/drives", "/api/chart-points", "/api/samples", "/api/sleep-activity", "/api/insights/efficiency"]

    nonisolated static func isScoped(_ path: String) -> Bool {
        let p = path.split(separator: "?", maxSplits: 1).first.map(String.init) ?? path
        return routes.contains { p == $0 || p.hasPrefix($0 + "/") || p.hasPrefix($0 + ".") }
    }

    func note(path: String, header: String?) {
        guard Self.isScoped(path) else { return }
        let next = (header?.isEmpty ?? true) ? nil : header
        if next != id { id = next }
    }

    func reset() { id = nil }

    /// The model, or the nickname only when two vehicles share a model (as the statement email names it).
    nonisolated static func name(_ vehicles: [Vehicle], id: String) -> String? {
        guard let v = vehicles.first(where: { $0.id == id }) else { return nil }
        let model = v.model.trimmingCharacters(in: .whitespaces)
        let shared = vehicles.filter { $0.model.trimmingCharacters(in: .whitespaces) == model }.count > 1
        let nick = v.name.trimmingCharacters(in: .whitespaces)
        if !model.isEmpty, !shared { return model }
        return nick.isEmpty ? (model.isEmpty ? nil : model) : nick
    }

    nonisolated static func sentence(_ name: String) -> String { "Showing \(name). Pro includes all vehicles." }
}
