import SwiftUI

/// The slot under the gauge (see NowSlotModel). Parked: the map, then the weather as one row. Charging and driving: the big figure, then the
/// session or drive in a grid. Read-only. The card's VoiceOver text takes the slot's own words (`NowSlotModel.spoken`), so what is said is
/// what is shown.
struct NowSlotView: View {
    let model: NowSlotModel
    /// The charging graph's card (now_charge_graph): the session figures are quiet label and value rows, as the web's.
    var soft = false
    /// The parked graph is the card's content (now_parked_graph): the map gives way to one line saying where the truck is, none when it is not known.
    var noMap = false

    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        switch model.content {
        case .parked(let lat, let lon, let label, let weather):
            VStack(alignment: .leading, spacing: 12) {
                if noMap {
                    if let place = model.place { placeLine(place) }
                } else {
                    ParkedMap(lat: lat, lon: lon, label: label)
                }
                if !model.sleep.isEmpty { grid(model.sleep).accessibilityIdentifier("now-slot-asleep") }
                if let weather { weatherRow(weather) }
            }
            .accessibilityIdentifier("now-slot-parked")
        case .figures(let big, let unit, let sub, let small, let stats):
            VStack(alignment: .leading, spacing: 8) {
                HStack(alignment: .firstTextBaseline, spacing: 4) {
                    Text(big).font(Theme.sans(ThemeTokens.typeHero, .semibold, relativeTo: .largeTitle)).foregroundStyle(Theme.textPrimary)
                    Text(unit).themeText(.value, color: Theme.textSecondary)
                    if let pack = model.pack {
                        (Text(pack).font(Theme.sans(ThemeTokens.typeTitle, .semibold, relativeTo: .title3)).foregroundColor(Theme.textPrimary)
                            + Text(" pack").font(Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body)).foregroundColor(Theme.textSecondary))
                            .monospacedDigit().padding(.leading, 8).accessibilityIdentifier("now-slot-pack")
                    }
                    ForEach(model.chips, id: \.text) { chip($0) }
                }
                if let sub { Text(sub).themeText(.value, color: Theme.textPrimary).fixedSize(horizontal: false, vertical: true) }
                if let small { Text(small).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true) }
                if !stats.isEmpty { (soft ? AnyView(rows(stats)) : AnyView(grid(stats))).padding(.top, 4) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityIdentifier("now-slot-figures")
        }
    }

    private func placeLine(_ text: String) -> some View {
        HStack(spacing: 8) {
            Image(systemName: "mappin.and.ellipse").font(.caption).foregroundStyle(Theme.accent).accessibilityHidden(true)
            Text(text).themeText(.value, color: Theme.textPrimary).lineLimit(1).truncationMode(.tail)
        }
        .padding(.horizontal, 12).padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("now-parked-place")
    }

    private func chip(_ c: NowSlotModel.Chip) -> some View {
        Text(c.text).themeText(.caption, color: c.accent ? Theme.accent : Theme.textSecondary)
            .padding(.horizontal, 8).padding(.vertical, 2)
            .background(Capsule().fill((c.accent ? Theme.accent : Theme.textSecondary).opacity(0.18)))
            .padding(.leading, 4)
    }

    private func rows(_ stats: [NowSlotModel.Stat]) -> some View {
        VStack(spacing: 0) {
            ForEach(stats, id: \.label) { s in
                HStack(alignment: .firstTextBaseline, spacing: 12) {
                    Text(s.label).themeText(.value, color: Theme.textSecondary)
                    Spacer(minLength: 8)
                    (Text(s.value).foregroundColor(Theme.textPrimary) + Text(s.note.map { " \($0)" } ?? "").font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption)).foregroundColor(Theme.textTertiary))
                        .font(Theme.sans(ThemeTokens.typeValue, .semibold, relativeTo: .body)).monospacedDigit().multilineTextAlignment(.trailing)
                }
                .padding(.vertical, 8)
                .overlay(alignment: .bottom) { Rectangle().fill(Theme.border.opacity(0.6)).frame(height: 1) }
                .accessibilityIdentifier("now-slot-row-\(s.label)")
            }
        }
    }

    private func grid(_ stats: [NowSlotModel.Stat]) -> some View {
        LazyVGrid(columns: [GridItem(.flexible(), alignment: .topLeading), GridItem(.flexible(), alignment: .topLeading)], alignment: .leading, spacing: 12) {
            ForEach(stats, id: \.label) { s in
                VStack(alignment: .leading, spacing: 2) {
                    Text(s.label).themeText(.label)
                    Text(s.value).themeText(.title).lineLimit(2).minimumScaleFactor(0.7)
                    if let note = s.note { Text(note).themeText(.caption, color: Theme.textTertiary) }
                }
            }
        }
    }

    /// Temperature and sky left, wind right; stacked at accessibility sizes.
    private func weatherRow(_ w: NowSlotModel.Weather) -> some View {
        let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(alignment: .center, spacing: 12))
        return layout {
            HStack(spacing: 8) {
                if let symbol = w.symbol { Image(systemName: symbol).font(.title2).foregroundStyle(Theme.estimate).accessibilityHidden(true) }
                VStack(alignment: .leading, spacing: 2) {
                    Text(w.temp).themeText(.title)
                    if let c = w.condition { Text(c).themeText(.subline, color: Theme.textSecondary) }
                }
            }
            if !typeSize.isAccessibilitySize { Spacer(minLength: 0) }
            VStack(alignment: typeSize.isAccessibilitySize ? .leading : .trailing, spacing: 2) {
                if let wind = w.wind { Text(wind) }
            }
            .themeText(.subline, color: Theme.textSecondary)
            .multilineTextAlignment(typeSize.isAccessibilitySize ? .leading : .trailing)
        }
    }
}
