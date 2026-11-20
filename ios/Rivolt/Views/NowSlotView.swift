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
            VStack(alignment: .leading, spacing: soft ? 2 : 8) {
                HStack(alignment: .firstTextBaseline, spacing: 4) {
                    Text(big).font(Theme.sans(soft ? ThemeTokens.typeTitle * 1.5 : ThemeTokens.typeHero, .semibold, relativeTo: .largeTitle)).foregroundStyle(Theme.textPrimary)
                    Text(unit).themeText(.value, color: Theme.textSecondary)
                    ForEach(model.chips, id: \.text) { chip($0) }
                }
                if let sub { Text(sub).themeText(.value, color: Theme.textPrimary).fixedSize(horizontal: false, vertical: true) }
                if let small { Text(small).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true) }
                if !stats.isEmpty { (soft ? AnyView(softGrid(stats)) : AnyView(grid(stats))).padding(.top, soft ? 6 : 4) }
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

    /// The soft card's figures: a tight two-column grid of label-over-value cells. When every cell but the elapsed time is our own estimate one quiet heading says so, inside the
    /// grid (it wraps at any width and text size); in a mixed set only the derived cells carry a small "est." (the cost always keeps the word "estimate" and its source in its note). The truck's own figures never
    /// carry a mark. VoiceOver says "estimated" with each estimated value either way.
    private func softGrid(_ stats: [NowSlotModel.Stat]) -> some View {
        let all = NowSlotModel.estimatedHeading(stats)
        return VStack(alignment: .leading, spacing: 8) {
            if all { Text("Estimated so far").themeText(.label).accessibilityIdentifier("now-slot-estimated") }
            LazyVGrid(columns: [GridItem(.flexible(), alignment: .topLeading), GridItem(.flexible(), alignment: .topLeading)], alignment: .leading, spacing: 8) {
                ForEach(stats, id: \.label) { s in
                    let mark = !all && s.estimated && s.label != "Cost so far" ? "est." : nil
                    VStack(alignment: .leading, spacing: 1) {
                        Text(s.label).themeText(.label)
                        (Text(s.value).foregroundColor(Theme.textPrimary)
                            + Text(mark.map { " \($0)" } ?? "").font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption)).foregroundColor(Theme.textTertiary))
                            .font(Theme.sans(ThemeTokens.typeValue, .semibold, relativeTo: .body)).monospacedDigit().lineLimit(2)
                        if let note = s.note { Text(note).themeText(.caption, color: Theme.textTertiary) }
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel("\(s.label), \(s.estimated ? "estimated " : "")\(SpokenText.words(s.value))\(s.note.map { ", \($0)" } ?? "")")
                    .accessibilityIdentifier("now-slot-row-\(s.label)")
                }
            }
        }
    }

    /// The unkeyed card's figures: the same marks as the soft grid (one heading when all our own, else a small "est." on the derived cells; the cost keeps "estimate" in its note).
    private func grid(_ stats: [NowSlotModel.Stat]) -> some View {
        let all = NowSlotModel.estimatedHeading(stats)
        return VStack(alignment: .leading, spacing: 8) {
            if all { Text("Estimated so far").themeText(.label).accessibilityIdentifier("now-slot-estimated") }
            LazyVGrid(columns: [GridItem(.flexible(), alignment: .topLeading), GridItem(.flexible(), alignment: .topLeading)], alignment: .leading, spacing: 12) {
                ForEach(stats, id: \.label) { s in
                    let mark = !all && s.estimated && s.label != "Cost so far" ? "est." : nil
                    VStack(alignment: .leading, spacing: 2) {
                        Text(s.label).themeText(.label)
                        (Text(s.value).foregroundColor(Theme.textPrimary)
                            + Text(mark.map { " \($0)" } ?? "").font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption)).foregroundColor(Theme.textTertiary))
                            .themeText(.title).lineLimit(2).minimumScaleFactor(0.7)
                        if let note = s.note { Text(note).themeText(.caption, color: Theme.textTertiary) }
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel("\(s.label), \(s.estimated ? "estimated " : "")\(SpokenText.words(s.value))\(s.note.map { ", \($0)" } ?? "")")
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
