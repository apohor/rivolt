import SwiftUI

/// Settings → Vehicle → "Vehicle the free plan shows". A free account with several vehicles shows one; the rest keep recording and are
/// listed as Pro. Nothing is hidden or deleted. Listed only where the server bills.
struct PrimaryVehicleView: View {
    @Environment(AppState.self) private var state
    @State private var vehicles: [Vehicle] = []
    @State private var primary: String?
    @State private var status: String?
    @State private var loaded = false

    var body: some View {
        SettingsStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 3 * Theme.unit) {
                    Text(ProCopy.primaryVehicleIntro).themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
                    if loaded && vehicles.count < 2 {
                        Text("You have one vehicle, so there is nothing to choose.").themeText(.value, color: Theme.textSecondary)
                    }
                    ForEach(vehicles.count < 2 ? [] : vehicles) { v in
                        Button { choose(v) } label: {
                            HStack {
                                Text(v.name).themeText(.value, color: Theme.textPrimary)
                                Spacer()
                                if v.id == primary { Image(systemName: "checkmark").foregroundStyle(Theme.accent).accessibilityLabel("Selected") }
                            }
                            .padding(3 * Theme.unit)
                            .frame(maxWidth: .infinity)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .accessibilityAddTraits(v.id == primary ? .isSelected : [])
                    }
                    if let status { Text(status).themeText(.subline, color: Theme.textSecondary).accessibilityAddTraits(.updatesFrequently) }
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Vehicle the free plan shows")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .confirmationAction) { SheetDoneButton() } }
        }
        .task {
            vehicles = (try? await state.client.vehicles()) ?? []
            primary = try? await state.client.primaryVehicle()
            loaded = true
        }
    }

    private func choose(_ v: Vehicle) {
        guard v.id != primary else { return }
        Task {
            do {
                try await state.client.setPrimaryVehicle(vehicleID: v.id)
                primary = v.id
                vehicles = vehicles.map { var c = $0; c.proLocked = $0.id != v.id; return c }
                status = ProCopy.primaryVehicleSaved(v.name)
            } catch {
                status = "That didn't save. Check your connection and try again."
            }
        }
    }
}
