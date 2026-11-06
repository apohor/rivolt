import SwiftUI

/// A signed-in plan, map first: the route on a full-height map that can be panned and zoomed, and the itinerary in a sheet with three resting
/// heights over its bottom. A pin and its row in the sheet are one selection: pick either and the other follows (the map eases to a stop that is
/// out of view, the sheet opens to its row). A two-day trip has day chips (and the "Driving per day" row) in the sheet.
struct PlanMapScreen: View {
    @Environment(AppState.self) private var state
    let model: TripPlanModel
    let saved: SavedTripsModel
    let onEdit: () -> Void

    @State private var detent = DetentSheet<EmptyView, EmptyView>.Detent.half
    @State private var day: Int?

    private var metric: Bool { NowFormat.usesMetric }

    var body: some View {
        GeometryReader { geo in
            ZStack(alignment: .bottom) {
                map(coverHeight: DetentSheet<EmptyView, EmptyView>.height(.half, in: geo.size.height))
                DetentSheet(detent: $detent, container: geo.size.height) {
                    header
                } content: {
                    ScrollViewReader { proxy in
                        VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                            TripPlanResult(model: model, metric: metric, showsMap: false, onlyDay: day)
                            if model.plan != nil { SaveTripSection(model: model, saved: saved) }
                        }
                        .onChange(of: model.focusedStop) { _, i in
                            guard let i else { return }
                            if detent == .peek { detent = .half }
                            withAnimation { proxy.scrollTo("stop-\(i)", anchor: .center) }
                        }
                    }
                }
            }
        }
        .ignoresSafeArea(.container, edges: .bottom)
    }

    @ViewBuilder
    private func map(coverHeight: CGFloat) -> some View {
        if case .planned(let trip) = model.night {
            PlanDaysMap(days: trip.days, focus: model.focusedStop, onStop: { model.focusedStop = $0 }, fills: true, bottomInset: coverHeight, day: day)
        } else if let route = model.plan?.best {
            PlanRouteMap(route: route, onStop: { model.focusedStop = $0 }, focus: model.focusedStop, along: model.chargers,
                         onLine: { model.roadLine = $0 }, fills: true, bottomInset: coverHeight)
        }
    }

    @ViewBuilder
    private var header: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(PlanSheetLogic.title(origin: model.origin?.line, destination: model.destination?.line)).themeText(.title, color: Theme.textPrimary)
                        .lineLimit(2).fixedSize(horizontal: false, vertical: true)
                    if let route = model.plan?.best {
                        Text(TripPlanLogic.summary(route, metric: metric)).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                    }
                }
                Spacer(minLength: 8)
                Button("Edit", action: onEdit).font(Theme.sans(ThemeTokens.typeValue, .medium, relativeTo: .body)).tint(Theme.accent)
                    .frame(minWidth: 44, minHeight: 44)
                    .accessibilityHint("Change the trip or open a saved one")
            }
            if case .planned(let trip) = model.night, trip.days.count > 1 { dayChips(trip) }
        }
    }

    private func dayChips(_ trip: TripMultiday) -> some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                chip("Whole trip", on: day == nil) { day = nil }
                ForEach(trip.days) { d in chip("Day \(d.index)", on: day == d.index) { day = d.index } }
            }
        }
    }

    private func chip(_ title: String, on: Bool, _ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title).themeText(.subline, color: on ? Theme.textPrimary : Theme.textSecondary)
                .padding(.horizontal, 12).frame(minHeight: 36)
                .background(on ? Theme.borderStrong : Theme.background, in: Capsule())
                .overlay(Capsule().strokeBorder(Theme.border, lineWidth: 1))
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(on ? .isSelected : [])
    }
}

enum PlanSheetLogic {
    /// "Seattle → Spokane": the sheet's title (the start is the truck's own place when none was picked).
    static func title(origin: String?, destination: String?) -> String {
        let to = destination.flatMap { $0.isEmpty ? nil : shortPlace($0) } ?? "Your trip"
        guard let origin, !origin.isEmpty else { return "To \(to)" }
        return "\(shortPlace(origin)) → \(to)"
    }

    /// The first part of a place line ("Seattle, Washington, United States" → "Seattle").
    static func shortPlace(_ line: String) -> String {
        line.split(separator: ",").first.map { $0.trimmingCharacters(in: .whitespaces) } ?? line
    }
}
