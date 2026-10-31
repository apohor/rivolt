import SwiftUI

/// The one-line note under a page the server narrowed to a free account's primary vehicle. Nothing when the answer was not narrowed.
struct VehicleScopeNote: View {
    let vehicles: [Vehicle]
    private var scope = VehicleScope.shared

    init(vehicles: [Vehicle]) { self.vehicles = vehicles }

    var body: some View {
        if let id = scope.id, let name = VehicleScope.name(vehicles, id: id) {
            Text(VehicleScope.sentence(name))
                .themeText(.subline, color: Theme.textSecondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityIdentifier("vehicle-scope-note")
        }
    }
}
