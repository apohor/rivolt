import SwiftUI

/// A merged round trip's legs, in order: when each ran, how far, and how long the truck sat before the next (the web's Legs card).
struct RoundTripLegsCard: View {
    let legs: [ActivityLeg]
    var calendar: Calendar = .current
    /// One row until a tap opens the list; the choice is kept on this iPhone (as the plan's elevation row).
    @AppStorage("rivolt.legsOpen") private var open = false

    /// "3 legs · parked 13 min, 19 min"; past three stops, "parked 4 times".
    static func summary(_ legs: [ActivityLeg]) -> String {
        let parked = legs.compactMap(\.parkedMinAfter)
        let head = "\(legs.count) legs"
        if parked.isEmpty { return head }
        if parked.count > 3 { return "\(head) · parked \(parked.count) times" }
        return "\(head) · parked " + parked.map { RouteSnap.parkedText(minutes: $0) }.joined(separator: ", ")
    }

    /// The same for VoiceOver, with the minutes in words.
    static func spokenSummary(_ legs: [ActivityLeg]) -> String {
        let parked = legs.compactMap(\.parkedMinAfter)
        let head = "\(legs.count) legs"
        if parked.isEmpty { return head }
        if parked.count > 3 { return "\(head), parked \(parked.count) times" }
        return "\(head), parked " + parked.map { "\($0) minutes" }.joined(separator: ", ")
    }

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
        VStack(alignment: .leading, spacing: 0) {
            Button { withAnimation(.easeOut(duration: 0.2)) { open.toggle() } } label: {
                HStack(spacing: 8) {
                    Text(Self.summary(legs)).themeText(.value, color: Theme.textBody)
                    Spacer(minLength: 8)
                    Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(Theme.textTertiary)
                        .rotationEffect(.degrees(open ? 90 : 0))
                }
                .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 2.5 * Theme.unit)
                .frame(minHeight: 44)
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(Self.spokenSummary(legs))
            .accessibilityValue(open ? "Legs shown" : "Legs hidden")
            .accessibilityHint(open ? "Hides the legs" : "Shows the legs")
            .accessibilityIdentifier("legs-toggle")
            if open { list }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
    }

    private var list: some View {
        VStack(alignment: .leading, spacing: 8) {
            Divider().overlay(Theme.border)
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
        .padding(.horizontal, 12).padding(.bottom, 12)
    }
}
