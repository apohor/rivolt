import SwiftUI

/// A merged round trip's legs, in order: when each ran, how far, and how long the truck sat before the next (the web's Legs card).
struct RoundTripLegsCard: View {
    let legs: [ActivityLeg]
    var calendar: Calendar = .current

    private func clock(_ d: Date) -> String {
        var f = Date.FormatStyle(timeZone: calendar.timeZone).hour().minute()
        f.calendar = calendar
        return d.formatted(f)
    }

    /// "Leg 1, 9:15 AM to 9:40 AM, 12 miles, parked 13 minutes after." for VoiceOver.
    static func spoken(_ leg: ActivityLeg, number: Int, from: String, to: String) -> String {
        var s = "Leg \(number), \(from) to \(to), \(NowFormat.distance(mi: leg.distanceMi, spoken: true))."
        if let m = leg.parkedMinAfter { s += " Parked \(m) minutes after." }
        return s
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Legs").themeText(.label)
            ForEach(Array(legs.enumerated()), id: \.element.id) { i, leg in
                VStack(alignment: .leading, spacing: 4) {
                    HStack {
                        Text("Leg \(i + 1)").themeText(.subline, color: Theme.textBody)
                        Spacer()
                        Text("\(clock(leg.startedAt)) to \(clock(leg.endedAt))  \(NowFormat.distance(mi: leg.distanceMi))").themeText(.subline)
                    }
                    if let m = leg.parkedMinAfter {
                        Text("Parked \(RouteSnap.parkedText(minutes: m))").themeText(.caption, color: Theme.textTertiary)
                    }
                }
                .accessibilityElement(children: .combine)
                .accessibilityLabel(Self.spoken(leg, number: i + 1, from: clock(leg.startedAt), to: clock(leg.endedAt)))
                .accessibilityIdentifier("leg-\(i + 1)")
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
    }
}
