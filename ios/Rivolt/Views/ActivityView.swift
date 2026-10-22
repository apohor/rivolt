import SwiftUI

/// The Activity tab: one month of the selected truck's drives and charges on a timeline, newest first and grouped by day,
/// with the month stepper and an All / Drives / Charges filter, as the web's feed. Read-only, figures only (no map, no
/// places). A row opens its detail.
struct ActivityView: View {
    @Environment(AppState.self) private var state
    @AppStorage("rivolt.selected_vehicle") private var selectedID = ""
    @State private var vehicles: [Vehicle] = []
    @State private var model = ActivityModel()
    @State private var month = PeriodMonth(containing: .now, calendar: .current)
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
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    ThemeSegmented(options: ActivityFilter.allCases.map { ThemeSegmented<ActivityFilter>.Option(id: $0, label: $0.label) }, selection: $filter)
                    status
                    ForEach(days) { day in
                        VStack(alignment: .leading, spacing: 12) {
                            ActivityDayHeader(day: day.day, calendar: calendar)
                            VStack(spacing: 0) {
                                ForEach(day.items) { item in
                                    NavigationLink {
                                        ActivityDetailView(item: item, calendar: calendar)
                                    } label: {
                                        ActivityFeedRow(item: item, calendar: calendar)
                                    }
                                    .buttonStyle(.plain)
                                }
                            }
                        }
                    }
                    PageFooter(server: state.serverURL)
                }
                .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 4 * Theme.unit)
            }
            .background(ThemePageBackground().ignoresSafeArea())
            .overlay {
                if model.isLoading && model.response == nil { ProgressView().controlSize(.large) }
            }
            .toolbar(.hidden, for: .navigationBar)
            .safeAreaInset(edge: .top, spacing: 0) {
                VStack(spacing: 0) {
                    ThemeTopBar { AccountMenu(vehicleID: vehicleID, onRefresh: { Task { await load(force: true) } }) }
                    titleRow
                }
            }
            .refreshable { await load(force: true) }
            .task(id: "\(vehicleID ?? "")|\(month.start.timeIntervalSince1970)") { await load(force: false) }
            .task { if vehicles.isEmpty { vehicles = (try? await state.client.vehicles()) ?? [] } }
        }
    }

    @ViewBuilder
    private var status: some View {
        if model.limited {
            Text("Slow down a little: Rivolt will refresh this later. Pull to try again.").themeText(.subline, color: Theme.estimate)
        } else if model.failed && model.response == nil {
            Text("Couldn't load this month. Pull to try again.").themeText(.subline, color: Theme.estimate)
        } else if model.response != nil && days.isEmpty {
            Text(emptyText).themeText(.subline)
        }
    }

    private var emptyText: String {
        let m = month.spokenTitle(calendar: calendar)
        switch filter {
        case .all: return "No drives or charges in \(m)."
        case .drives: return "No drives in \(m)."
        case .charges: return "No charges in \(m)."
        }
    }

    private var titleRow: some View {
        HStack(alignment: .center, spacing: 8) {
            Text("Activity").themeText(.hero).accessibilityAddTraits(.isHeader)
            Spacer(minLength: 8)
            ThemeStepper(title: month.title(calendar: calendar), canGoForward: !month.isCurrent(now: .now, calendar: calendar),
                         previous: { month = month.shifted(by: -1, calendar: calendar) },
                         next: { month = month.shifted(by: 1, calendar: calendar) })
        }
        .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 2 * Theme.unit)
        .background(Theme.background)
        .overlay(alignment: .bottom) { Rectangle().fill(Theme.border).frame(height: 1) }
    }

    private func load(force: Bool) async {
        if vehicles.isEmpty { vehicles = (try? await state.client.vehicles()) ?? [] }
        guard let id = vehicleID else { return }
        await model.load(source: state.client, vehicleID: id, month: month, force: force, calendar: calendar)
    }
}
