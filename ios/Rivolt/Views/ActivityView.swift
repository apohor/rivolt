import SwiftUI

/// The Activity tab: one month of the selected truck's drives and charges, newest first and grouped by day, with the
/// month and an All / Drives / Charges filter. Read-only, figures only (no map, no places).
struct ActivityView: View {
    @Environment(AppState.self) private var state
    @AppStorage("rivolt.selected_vehicle") private var selectedID = ""
    @State private var vehicles: [Vehicle] = []
    @State private var model = ActivityModel()
    @State private var month = ActivityMonth(containing: .now, calendar: .current)
    @State private var filter = ActivityFilter.all
    private let calendar = Calendar.current

    private var vehicleID: String? {
        vehicles.first { $0.id == selectedID }?.id ?? vehicles.first?.id
    }

    private var days: [ActivityDay] {
        guard let r = model.response else { return [] }
        return ActivityLogic.days(r, filter: filter, calendar: calendar)
    }

    var body: some View {
        NavigationStack {
            List {
                Section {
                    Picker("Show", selection: $filter) {
                        ForEach(ActivityFilter.allCases) { Text($0.label).tag($0) }
                    }
                    .pickerStyle(.segmented)
                }
                .listRowBackground(Color.clear)
                if model.limited {
                    Text("Slow down a little: Rivolt will refresh this later. Pull to try again.")
                        .font(.footnote).foregroundStyle(NowInk.stale)
                } else if model.failed && model.response == nil {
                    Text("Couldn't load this month. Pull to try again.").font(.footnote).foregroundStyle(NowInk.stale)
                }
                if model.response != nil && days.isEmpty {
                    Text(emptyText).font(.footnote).foregroundStyle(NowInk.secondary)
                }
                ForEach(days) { day in
                    Section {
                        ForEach(day.items) { item in
                            NavigationLink {
                                ActivityDetailView(item: item, calendar: calendar)
                            } label: {
                                switch item {
                                case .drive(let d): ActivityDriveRow(drive: d, calendar: calendar)
                                case .charge(let c): ActivityChargeRow(charge: c, calendar: calendar)
                                }
                            }
                        }
                    } header: {
                        Text(day.day, format: .dateTime.weekday(.abbreviated).month(.abbreviated).day())
                    }
                }
            }
            .overlay {
                if model.isLoading && model.response == nil { ProgressView().controlSize(.large) }
            }
            .navigationTitle("Activity")
            .toolbar {
                ToolbarItemGroup(placement: .principal) { monthSwitcher }
            }
            .refreshable { await load(force: true) }
            .task(id: "\(vehicleID ?? "")|\(month.start.timeIntervalSince1970)") { await load(force: false) }
            .task { if vehicles.isEmpty { vehicles = (try? await state.client.vehicles()) ?? [] } }
        }
    }

    private var emptyText: String {
        switch filter {
        case .all: return "No drives or charges in \(month.title(calendar: calendar))."
        case .drives: return "No drives in \(month.title(calendar: calendar))."
        case .charges: return "No charges in \(month.title(calendar: calendar))."
        }
    }

    private var monthSwitcher: some View {
        HStack(spacing: 4) {
            Button { month = month.shifted(by: -1, calendar: calendar) } label: { Image(systemName: "chevron.left") }
                .accessibilityLabel("Previous month")
            Text(month.title(calendar: calendar)).font(.headline).frame(minWidth: 140)
            Button { month = month.shifted(by: 1, calendar: calendar) } label: { Image(systemName: "chevron.right") }
                .disabled(month.isCurrent(now: .now, calendar: calendar))
                .accessibilityLabel("Next month")
        }
    }

    private func load(force: Bool) async {
        if vehicles.isEmpty { vehicles = (try? await state.client.vehicles()) ?? [] }
        guard let id = vehicleID else { return }
        await model.load(source: state.client, vehicleID: id, month: month, force: force, calendar: calendar)
    }
}
