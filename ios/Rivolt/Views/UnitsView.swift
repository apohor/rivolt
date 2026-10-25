import SwiftUI

/// Units: miles or kilometres, and °F or °C (or following the distance). They are the account's, so the web and the phone agree.
struct UnitsView: View {
    @Environment(AppState.self) private var state
    @Environment(\.dismiss) private var dismiss

    enum Temperature: String, CaseIterable, Identifiable {
        case auto, c, f
        var id: String { rawValue }
        var title: String { self == .auto ? "Follow distance" : self == .c ? "°C" : "°F" }
    }

    @State private var distance = DisplayUnits.distanceMetric ? "km" : "mi"
    @State private var temperature = DisplayUnits.temperatureFollowsDistance ? Temperature.auto : (DisplayUnits.celsius ? .c : .f)
    @State private var failed = false

    var body: some View {
        VStack(alignment: .leading, spacing: 5 * Theme.unit) {
            Text("Units").font(Theme.sans(20, .semibold)).foregroundStyle(Theme.textPrimary).accessibilityAddTraits(.isHeader)
            Text("Saved to your account, so the web and the iPhone app agree.").themeText(.value, color: Theme.textBody)
                .fixedSize(horizontal: false, vertical: true)
            VStack(alignment: .leading, spacing: 2 * Theme.unit) {
                Text("Distance").themeText(.subline, color: Theme.textTertiary)
                Picker("Distance", selection: $distance) {
                    Text("Miles").tag("mi")
                    Text("Kilometres").tag("km")
                }
                .pickerStyle(.segmented)
            }
            VStack(alignment: .leading, spacing: 2 * Theme.unit) {
                Text("Temperature").themeText(.subline, color: Theme.textTertiary)
                Picker("Temperature", selection: $temperature) {
                    ForEach(Temperature.allCases) { Text($0.title).tag($0) }
                }
                .pickerStyle(.segmented)
            }
            if failed {
                Text("That didn't save. Check your connection and try again.").themeText(.subline, color: Theme.estimate)
            }
            Button("Done") { dismiss() }.buttonStyle(ThemeButtonStyle())
            Spacer(minLength: 0)
        }
        .padding(6 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
        .themePage()
        .presentationDetents([.medium])
        .onDisappear { state.unitsScreenClosed() }
        .onChange(of: distance) { _, new in save(distance: new) }
        .onChange(of: temperature) { _, new in save(temperature: new.rawValue) }
    }

    private func save(distance: String? = nil, temperature: String? = nil) {
        Task {
            let ok = await state.setUnits(distance: distance, temperature: temperature)
            failed = !ok
            if !ok {
                // Show what is really in force.
                self.distance = DisplayUnits.distanceMetric ? "km" : "mi"
                self.temperature = DisplayUnits.temperatureFollowsDistance ? .auto : (DisplayUnits.celsius ? .c : .f)
            }
        }
    }
}
