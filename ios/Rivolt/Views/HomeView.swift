import SwiftUI

/// Single-vehicle home screen: hero SoC + range number for the first
/// (or only) vehicle on the account. Intentionally dumb — this is the
/// v0.9-day-one smoke screen, not the final layout. The real home
/// will mirror the web `/` overview once the API surface for drives
/// and charges is available.
///
/// Refresh strategy:
///   • On appear: one `/vehicles` + one `/state/:id` call.
///   • Pull-to-refresh to re-fetch without leaving the screen.
///
/// Polling + websocket will land with the live panel; for now we
/// keep it manual so we don't bake polling into the v0.9 skeleton
/// before we've thought about battery cost on real device.
struct HomeView: View {
    @Environment(AppState.self) private var state

    @State private var vehicles: [Vehicle] = []
    @State private var current: VehicleState?
    @State private var loadError: String?
    @State private var isLoading = false

    var body: some View {
        NavigationStack {
            Group {
                if isLoading && current == nil {
                    ProgressView().controlSize(.large)
                } else if let current, let vehicle = vehicles.first {
                    content(vehicle: vehicle, st: current)
                } else if let loadError {
                    VStack(spacing: 8) {
                        Image(systemName: "exclamationmark.triangle")
                            .font(.title)
                        Text(loadError).multilineTextAlignment(.center)
                    }
                    .foregroundStyle(.secondary)
                    .padding()
                } else {
                    Text("No vehicles on this account")
                        .foregroundStyle(.secondary)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .navigationTitle(vehicles.first?.name ?? "Rivolt")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Menu {
                        Button("Refresh") { Task { await load() } }
                        Divider()
                        Button("Sign out", role: .destructive) {
                            Task { await state.logout() }
                        }
                    } label: {
                        Image(systemName: "person.crop.circle")
                    }
                }
            }
            .refreshable { await load() }
            .task { await load() }
        }
    }

    @ViewBuilder
    private func content(vehicle: Vehicle, st: VehicleState) -> some View {
        VStack(spacing: 24) {
            VStack(spacing: 4) {
                Text("\(Int(st.batteryLevelPct.rounded()))%")
                    .font(.system(size: 80, weight: .semibold, design: .rounded))
                    .monospacedDigit()
                Text("state of charge")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }

            ProgressView(value: min(max(st.batteryLevelPct, 0), 100), total: 100)
                .progressViewStyle(.linear)
                .tint(batteryTint(st.batteryLevelPct))
                .padding(.horizontal, 32)

            VStack(spacing: 2) {
                Text("\(Int(rangeMiles(km: st.distanceToEmpty).rounded())) mi")
                    .font(.title)
                    .monospacedDigit()
                Text("estimated range")
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }

            if let charger = st.chargerState, charger != "charger_disconnected", charger != "" {
                Label(formatCharger(charger, powerKW: st.chargerPowerKw), systemImage: "bolt.fill")
                    .foregroundStyle(.green)
                    .font(.subheadline)
            }

            Spacer()

            VStack(alignment: .leading, spacing: 4) {
                Text(vehicle.model)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Text("VIN \(vehicle.vin.suffix(8))")
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
                    .monospaced()
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal)
        }
        .padding(.top, 32)
    }

    private func load() async {
        isLoading = true
        defer { isLoading = false }
        loadError = nil
        do {
            let vs = try await state.client.vehicles()
            self.vehicles = vs
            guard let first = vs.first else {
                self.current = nil
                return
            }
            self.current = try await state.client.vehicleState(id: first.id)
        } catch {
            self.loadError = error.localizedDescription
        }
    }

    private func batteryTint(_ pct: Double) -> Color {
        switch pct {
        case ..<15: return .red
        case ..<30: return .orange
        default: return .green
        }
    }

    private func rangeMiles(km: Double) -> Double { km * 0.621371 }

    private func formatCharger(_ raw: String, powerKW: Double?) -> String {
        let label: String
        switch raw {
        case "charging_active":  label = "Charging"
        case "charging_complete": label = "Charge complete"
        case "charger_connected": label = "Plug connected"
        default:
            label = raw
                .replacingOccurrences(of: "_", with: " ")
                .capitalized
        }
        if let kw = powerKW, kw > 0.1 {
            return String(format: "%@ · %.1f kW", label, kw)
        }
        return label
    }
}
