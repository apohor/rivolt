import SwiftUI

/// The Activity tab: one month of the selected truck's drives and charges on a timeline, newest first and grouped by day,
/// with the month stepper and an All / Drives / Charges filter, as the web's feed; or (List / Map / Efficiency) the
/// month's routes and charges on our own map, or its efficiency breakdowns, as the web's Activity views. Read-only. A
/// row, a route or a dot opens its detail.
struct ActivityView: View {
    @Environment(AppState.self) private var state
    @AppStorage("rivolt.selected_vehicle") private var selectedID = ""
    @State private var vehicles: [Vehicle] = []
    @State private var model = ActivityModel()
    @State private var month = PeriodMonth(containing: .now, calendar: .current)
    @State private var filter = ActivityFilter.all
    /// List every launch (not kept): the accessible view first, and no map read on a slow connection unless asked.
    @State private var mode = ActivityMode.list
    @State private var picked: ActivityItem?
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
            Group {
                switch mode {
                case .list: listBody
                case .map: mapBody
                case .efficiency: efficiencyBody
                }
            }
            .background(ThemePageBackground().ignoresSafeArea())
            .overlay {
                if mode == .list && model.isLoading && model.response == nil { ProgressView().controlSize(.large) }
            }
            .toolbar(.hidden, for: .navigationBar)
            .safeAreaInset(edge: .top, spacing: 0) {
                VStack(spacing: 0) {
                    // Refresh reloads the map too: map mode has no pull-to-refresh.
                    ThemeTopBar {
                        AccountMenu(vehicleID: vehicleID, onRefresh: {
                            Task { await load(force: true); await loadMap(force: true); await loadEfficiency(force: true) }
                        })
                    }
                    titleRow
                }
            }
            // A tap on the map opens the drive or charge, as a row does.
            .navigationDestination(item: $picked) { ActivityDetailView(item: $0, calendar: calendar) }
            .task(id: "\(vehicleID ?? "")|\(month.start.timeIntervalSince1970)") { await load(force: false) }
            .task(id: "\(mode.rawValue)|\(vehicleID ?? "")|\(month.start.timeIntervalSince1970)") {
                await loadMap()
                await loadEfficiency()
            }
            .task { if vehicles.isEmpty { vehicles = (try? await state.client.vehicles()) ?? [] } }
            // App Store screenshots only: open the first drive or charge of the month once the list is there.
            .task(id: days.count) {
                guard let kind = StoreShots.open, picked == nil, !days.isEmpty else { return }
                picked = days.flatMap(\.items).first { item in
                    if case .drive = item { return kind == "drive" }
                    if case .charge = item { return kind == "charge" }
                    return false
                }
            }
        }
    }

    private var controls: some View { ActivityControls(mode: $mode, filter: $filter) }

    private var listBody: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                controls
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
            }
            .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 4 * Theme.unit)
        }
        .refreshable { await load(force: true) }
    }

    /// The month's efficiency breakdowns (the web's Activity "Efficiency").
    private var efficiencyBody: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                controls
                EfficiencyView(shown: state.efficiency.shown, monthName: month.spokenTitle(calendar: calendar))
            }
            .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 4 * Theme.unit)
        }
        .refreshable { await loadEfficiency(force: true) }
    }

    /// The month on the map: the screen's main content, pannable and zoomable.
    private var mapBody: some View {
        let map = state.activityMap
        let shown = (filter != .charges && !map.drives.isEmpty) || (filter != .drives && !map.charges.isEmpty)
        return VStack(alignment: .leading, spacing: 16) {
            controls
            if map.failed {
                Text("Couldn't load the map. Switch to List and back to try again.").themeText(.subline, color: Theme.estimate)
            } else if shown {
                ActivityMapView(drives: map.drives, charges: map.charges, filter: filter) { pick in
                    if let r = model.response { picked = ActivityLogic.item(for: pick, in: r) }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if map.isLoading || map.key == nil {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                Text(emptyText).themeText(.subline)
            }
            if !shown { Spacer(minLength: 0) }
        }
        .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 4 * Theme.unit)
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
        ThemeTitleRow(title: "Activity") {
            ThemeStepper(title: month.title(calendar: calendar), spokenTitle: month.spokenTitle(calendar: calendar), canGoForward: !month.isCurrent(now: .now, calendar: calendar),
                         previous: { month = month.shifted(by: -1, calendar: calendar) },
                         next: { month = month.shifted(by: 1, calendar: calendar) })
        }
    }

    /// The map's lists, read only when the Map view is open (the web's Activity map lists).
    private func loadMap(force: Bool = false) async {
        guard mode == .map else { return }
        if vehicles.isEmpty { vehicles = (try? await state.client.vehicles()) ?? [] }
        guard let id = vehicleID else { return }
        await state.activityMap.load(source: state.client, vehicleID: id, month: month, force: force, calendar: calendar)
    }

    /// Efficiency for the month, read only when that view is open.
    private func loadEfficiency(force: Bool = false) async {
        guard mode == .efficiency else { return }
        if vehicles.isEmpty { vehicles = (try? await state.client.vehicles()) ?? [] }
        guard let id = vehicleID else { return }
        await state.efficiency.load(source: state.client, vehicleID: id, month: month, metric: NowFormat.usesMetric, force: force, calendar: calendar)
    }

    private func load(force: Bool) async {
        if vehicles.isEmpty { vehicles = (try? await state.client.vehicles()) ?? [] }
        guard let id = vehicleID else { return }
        await model.load(source: state.client, vehicleID: id, month: month, force: force, calendar: calendar)
    }
}

/// The view (List, Map, Efficiency) on a row of its own, the filter (All, Drives, Charges) under it; no filter in
/// Efficiency, which is drives only.
struct ActivityControls: View {
    @Binding var mode: ActivityMode
    @Binding var filter: ActivityFilter
    /// ImageRenderer can't draw a system Menu (it paints a placeholder), so the snapshot test shows the button the menu opens from.
    var drawsMenuAsButton = false
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        VStack(alignment: .leading, spacing: 2 * Theme.unit) {
            if typeSize.isAccessibilitySize {
                viewMenu
            } else {
                ThemeSegmented(options: ActivityMode.allCases.map { ThemeSegmented<ActivityMode>.Option(id: $0, label: $0.label) }, selection: $mode,
                               accessibilityName: "View", fillsWidth: true)
            }
            if mode != .efficiency {
                ThemeSegmented(options: ActivityFilter.allCases.map { ThemeSegmented<ActivityFilter>.Option(id: $0, label: $0.label) }, selection: $filter)
            }
        }
    }

    /// At accessibility text sizes the three views would stack as three full-width rows and push every item below the fold
    /// (the scroll audit), so the view control is one button that opens a menu: "View: List".
    @ViewBuilder
    private var viewMenu: some View {
        if drawsMenuAsButton {
            viewMenuLabel
        } else {
            Menu {
                Picker("View", selection: $mode) {
                    ForEach(ActivityMode.allCases) { Text($0.label).tag($0) }
                }
            } label: {
                viewMenuLabel
            }
            .accessibilityLabel("View, \(mode.label)")
            .accessibilityHint("Choose List, Map or Efficiency")
        }
    }

    private var viewMenuLabel: some View {
            HStack(spacing: 2 * Theme.unit) {
                Text("View: \(mode.label)")
                    .font(Theme.sans(ThemeTokens.typeValue, .medium, relativeTo: .body))
                    .foregroundStyle(Theme.textPrimary)
                Image(systemName: "chevron.up.chevron.down").font(.footnote).foregroundStyle(Theme.textSecondary)
            }
            .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 2 * Theme.unit)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.card.opacity(0.5), in: RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
            .contentShape(Rectangle())
    }
}
