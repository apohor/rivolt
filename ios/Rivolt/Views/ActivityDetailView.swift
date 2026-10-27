import SwiftUI

/// One drive or charge: tiles like the web's detail page, over our own map: a drive's route (DriveRouteMap) or where
/// a charge happened (ChargeLocationMap), each fetched when shown and kept in memory only. The back link and the title sit under the same
/// top bar as every screen.
struct ActivityDetailView: View {
    let item: ActivityItem
    var calendar: Calendar = .current

    @Environment(\.dismiss) private var dismiss
    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(AppState.self) private var state
    /// A drive's row and samples, read once for its route map and timeline.
    @State private var driveDetail = DriveDetailModel()
    /// A charge's row, read once for its map and its session card.
    @State private var chargeRow = ChargeRowModel()
    @State private var sharing: ActivityDrive?

    private var title: String {
        switch item {
        case .drive(let d): return (d.roundTripLegs ?? 0) > 1 ? "Round trip" : "Drive"
        case .charge(let c): return ActivityLogic.chargeTitle(c)
        }
    }

    private var tiles: [KpiTile] {
        switch item {
        case .drive(let d):
            var out = ActivityLogic.tiles(d, metric: NowFormat.usesMetric)
            // The web's "Regen recovered", once the samples are in: after Energy.
            if let regen = driveDetail.timeline?.regenTile {
                out.insert(regen, at: (out.firstIndex { $0.id == "energy" }).map { $0 + 1 } ?? out.count)
            }
            return out
        case .charge(let c): return ActivityLogic.tiles(c)
        }
    }

    private var when: String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).weekday(.wide).month(.wide).day().hour().minute()
        f.calendar = calendar
        return item.startedAt.formatted(f)
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                Text(when).themeText(.subline)
                if case .drive(let d) = item, d.hasTrailer {
                    Text("Trailer connected. This drive is left out of the efficiency figures.").themeText(.subline, color: Theme.textTertiary)
                }
                if case .drive = item { DriveRouteMap(phase: driveDetail.phase) }
                if case .charge(let c) = item { ChargeLocationMap(chargeID: c.id, startedAt: c.startedAt, vehicleID: c.vehicleId, row: chargeRow.phase, calendar: calendar) }
                let columns = typeSize.isAccessibilitySize
                    ? [GridItem(.flexible())]
                    : [GridItem(.flexible(), spacing: 12, alignment: .top), GridItem(.flexible(), spacing: 12, alignment: .top)]
                LazyVGrid(columns: columns, spacing: 12) {
                    ForEach(tiles) { KpiTileView(tile: $0) }
                }
                if case .drive(let d) = item {
                    DriveTimelineCard(drive: d, phase: driveDetail.phase, data: driveDetail.timeline, calendar: calendar)
                }
                if case .charge(let c) = item { ChargeDetailSections(charge: c, row: chargeRow.phase.row, calendar: calendar) }
            }
            .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 4 * Theme.unit)
        }
        .background(ThemePageBackground().ignoresSafeArea())
        .task(id: item.id) {
            if case .drive(let d) = item { await driveDetail.load(source: state.client, drive: d) }
            if case .charge(let c) = item { await chargeRow.load(source: state.client, id: c.id) }
        }
        .toolbar(.hidden, for: .navigationBar)
        .sheet(item: $sharing) { d in
            ShareCardSheet(fileName: MoneyShareCard.fileName(kind: "trip", date: d.startedAt, calendar: calendar),
                           costHint: (d.estimatedCost ?? 0) > 0 ? "Adds the estimated charging cost." : nil) { cost in
                MoneyShareCard.trip(d, includeCost: cost, calendar: calendar)
            }
            .followsAppearance()
        }
        .safeAreaInset(edge: .top, spacing: 0) {
            VStack(spacing: 0) {
                ThemeTopBar { AccountMenu(vehicleID: nil, onRefresh: nil) }
                HStack(alignment: .center) {
                    Text(title).themeText(.hero).accessibilityAddTraits(.isHeader)
                    Spacer(minLength: 8)
                    if case .drive(let d) = item {
                        Button("Share") { sharing = d }
                            .font(Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body))
                            .foregroundStyle(Theme.textBody)
                            .accessibilityLabel("Share this trip")
                    }
                    Button { dismiss() } label: {
                        Label(backLabel, systemImage: "arrow.left").font(Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body))
                    }
                    .foregroundStyle(Theme.textBody)
                }
                .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 2 * Theme.unit)
                .background(Theme.background)
                .overlay(alignment: .bottom) { Rectangle().fill(Theme.border).frame(height: 1) }
            }
        }
    }

    private var backLabel: String {
        switch item {
        case .drive: return "all drives"
        case .charge: return "all charges"
        }
    }
}
