import SwiftUI

/// One drive or charge: tiles like the web's detail page, over our own map: a drive's route (DriveRouteMap) or where
/// a charge happened (ChargeLocationMap), each fetched when shown and kept in memory only. The back link and the title sit under the same
/// top bar as every screen.
struct ActivityDetailView: View {
    let item: ActivityItem
    var calendar: Calendar = .current

    @Environment(\.dismiss) private var dismiss
    @Environment(\.dynamicTypeSize) private var typeSize

    private var title: String {
        switch item {
        case .drive(let d): return (d.roundTripLegs ?? 0) > 1 ? "Round trip" : "Drive"
        case .charge(let c): return ActivityLogic.chargeTitle(c)
        }
    }

    private var tiles: [KpiTile] {
        switch item {
        case .drive(let d): return ActivityLogic.tiles(d, metric: NowFormat.usesMetric)
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
                if case .drive(let d) = item { DriveRouteMap(driveID: d.id) }
                if case .charge(let c) = item { ChargeLocationMap(chargeID: c.id) }
                let columns = typeSize.isAccessibilitySize
                    ? [GridItem(.flexible())]
                    : [GridItem(.flexible(), spacing: 12, alignment: .top), GridItem(.flexible(), spacing: 12, alignment: .top)]
                LazyVGrid(columns: columns, spacing: 12) {
                    ForEach(tiles) { KpiTileView(tile: $0) }
                }
            }
            .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 4 * Theme.unit)
        }
        .background(ThemePageBackground().ignoresSafeArea())
        .toolbar(.hidden, for: .navigationBar)
        .safeAreaInset(edge: .top, spacing: 0) {
            VStack(spacing: 0) {
                ThemeTopBar { AccountMenu(vehicleID: nil, onRefresh: nil) }
                HStack(alignment: .center) {
                    Text(title).themeText(.hero).accessibilityAddTraits(.isHeader)
                    Spacer(minLength: 8)
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
