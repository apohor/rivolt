import SwiftUI

/// The charging networks the person prefers, picked where they plan: the same "Prefer in planner" flag as Settings → Charging networks (the same
/// list, saved the same way), and the trip is planned again when the sheet is closed with a change. The planner favours a preferred network; it does
/// not promise one.
struct PlannerNetworksSheet: View {
    @Environment(AppState.self) private var state
    @Environment(\.dismiss) private var dismiss
    /// Called after a changed list was saved, to plan the trip again.
    let onChanged: () -> Void

    @State private var saved: [ChargingNetwork] = []
    @State private var rows: [ChargingNetwork] = []
    @State private var phase = Phase.loading
    @State private var saving = false
    @State private var problem: String?

    enum Phase { case loading, ready, failed }

    static let honesty = "Preferred networks are favoured by the planner, not guaranteed. Where one isn't on the way, you still get another."
    static let explanation = "The same list as Settings → Charging networks. Closing this with a change plans the trip again."

    /// The built-in networks (the ones the planner can be told about), preferred first, then by name.
    static func listed(_ rows: [ChargingNetwork]) -> [Int] {
        rows.indices.filter { !(rows[$0].slug ?? "").isEmpty }.sorted { a, b in
            let pa = rows[a].preferred == true, pb = rows[b].preferred == true
            if pa != pb { return pa }
            return rows[a].name.localizedCaseInsensitiveCompare(rows[b].name) == .orderedAscending
        }
    }

    /// Whether the preferred flags differ from what was saved.
    static func changed(_ rows: [ChargingNetwork], from saved: [ChargingNetwork]) -> Bool {
        rows.map { $0.preferred == true } != saved.map { $0.preferred == true }
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 3 * Theme.unit) {
                    Text(Self.honesty).themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
                    Text(Self.explanation).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                    switch phase {
                    case .loading:
                        ProgressView().tint(Theme.textSecondary).frame(maxWidth: .infinity).padding(.vertical, 6 * Theme.unit)
                    case .failed:
                        Text("Couldn't load your networks.").themeText(.value, color: Theme.estimate)
                        Button("Try again") { Task { await load() } }.buttonStyle(ThemeSecondaryButtonStyle())
                    case .ready:
                        ThemeSection(title: "Prefer in planner") {
                            ForEach(Array(Self.listed(rows).enumerated()), id: \.element) { n, i in
                                if n > 0 { Divider().overlay(Theme.border) }
                                Toggle(isOn: Binding(get: { rows[i].preferred == true }, set: { rows[i].preferred = $0 })) {
                                    Text("\(rows[i].preferred == true ? "★" : "☆") \(rows[i].name)").themeText(.value, color: Theme.textPrimary)
                                }
                                .tint(Theme.accent).frame(minHeight: 44)
                            }
                        }
                    }
                    if let problem { Text(problem).themeText(.subline, color: Theme.estimate).fixedSize(horizontal: false, vertical: true) }
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Networks")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button(saving ? "Saving…" : "Done") { Task { await done() } }.tint(Theme.accent).disabled(saving)
                }
            }
        }
        .task { await load() }
    }

    private func load() async {
        phase = .loading
        do {
            saved = try await state.client.chargingNetworks()
            rows = saved
            phase = .ready
        } catch {
            phase = .failed
        }
    }

    private func done() async {
        guard phase == .ready, Self.changed(rows, from: saved) else { dismiss(); return }
        saving = true
        problem = nil
        defer { saving = false }
        do {
            _ = try await state.client.setChargingNetworks(rows)
            dismiss()
            onChanged()
        } catch {
            problem = "Couldn't save your networks. Nothing was changed."
        }
    }
}
