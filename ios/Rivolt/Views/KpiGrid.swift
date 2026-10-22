import SwiftUI

/// The six tiles under the Now hero, as the web's: since the last charge, miles, energy added, efficiency, charging cost
/// and idle-awake time, for the month the stepper shows. Two columns, one at accessibility text sizes.
struct KpiGrid: View {
    let tiles: [KpiTile]
    let idle: IdleAwakeTile?
    let idleLoading: Bool
    @Environment(\.dynamicTypeSize) private var typeSize

    /// The tiles in the order the web lays them out, the idle tile last.
    private var cells: [AnyView] {
        tiles.map { AnyView(KpiTileView(tile: $0)) } + [AnyView(IdleAwakeTileView(tile: idle, loading: idleLoading))]
    }

    var body: some View {
        if typeSize.isAccessibilitySize {
            VStack(spacing: 12) {
                ForEach(tiles) { KpiTileView(tile: $0) }
                IdleAwakeTileView(tile: idle, loading: idleLoading)
            }
        } else {
            // A grid, not a lazy one: the two tiles of a row take the height of the taller, as the web's do.
            Grid(horizontalSpacing: 12, verticalSpacing: 12) {
                ForEach(Array(stride(from: 0, to: cells.count, by: 2)), id: \.self) { i in
                    GridRow {
                        cells[i]
                        if i + 1 < cells.count { cells[i + 1] } else { Color.clear }
                    }
                }
            }
        }
    }
}

struct KpiTileView: View {
    let tile: KpiTile

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(tile.label).themeText(.subline, color: Theme.textTertiary)
            HStack(alignment: .firstTextBaseline, spacing: 4) {
                Text(tile.value).themeText(.title)
                if !tile.unit.isEmpty { Text(tile.unit).themeText(.subline, color: Theme.textSecondary) }
            }
            .lineLimit(1).minimumScaleFactor(0.7)
            if let hint = tile.hint {
                Text(hint).themeText(.caption, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
            }
        }
        .themeCard(padding: 3 * Theme.unit, fillHeight: true)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(tile.spoken)
    }
}

/// Today's idle-awake time with the average over completed days and a seven-day sparkline.
struct IdleAwakeTileView: View {
    let tile: IdleAwakeTile?
    let loading: Bool

    private var spoken: String {
        guard let tile, !tile.bars.isEmpty else { return "Idle awake, \(loading ? "loading" : "collecting data")" }
        let today = tile.today.map { NowFormat.spokenDuration($0 * 60) } ?? "no figure yet"
        let avg = tile.average.map { "average \(NowFormat.spokenDuration($0 * 60)) per full day" } ?? "no full days yet"
        return "Idle awake today, \(today), \(avg)"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Idle awake").themeText(.subline, color: Theme.textTertiary)
            if let tile, !tile.bars.isEmpty {
                Text(tile.today.map { NowFormat.duration($0 * 60) } ?? "—").themeText(.title).lineLimit(1).minimumScaleFactor(0.7)
                Text("today · " + (tile.average.map { "avg \(NowFormat.duration($0 * 60))" } ?? "no full days yet"))
                    .themeText(.caption, color: Theme.textTertiary)
                Sparkline(bars: tile.bars).frame(height: 16).padding(.top, 2)
            } else {
                Text(loading ? "Loading…" : "Collecting data").themeText(.value, color: Theme.textSecondary)
            }
        }
        .themeCard(padding: 3 * Theme.unit, fillHeight: true)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken)
    }

    /// The last seven days as bars, the newest at full strength.
    private struct Sparkline: View {
        let bars: [DayValue]

        var body: some View {
            let peak = max(1, bars.map(\.value).max() ?? 1)
            GeometryReader { geo in
                let gap: CGFloat = 2
                let w = (geo.size.width - gap * CGFloat(max(bars.count - 1, 0))) / CGFloat(max(bars.count, 1))
                HStack(alignment: .bottom, spacing: gap) {
                    ForEach(Array(bars.enumerated()), id: \.offset) { i, b in
                        RoundedRectangle(cornerRadius: 1.5)
                            .fill(Theme.chartIdleAwakeTile.opacity(i == bars.count - 1 ? 1 : 0.55))
                            .frame(width: w, height: max(1.5, geo.size.height * CGFloat(b.value / peak)))
                    }
                }
                .frame(maxHeight: .infinity, alignment: .bottom)
            }
            .accessibilityHidden(true)
        }
    }
}
