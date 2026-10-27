import SwiftUI

/// The period picker beside the title of Now, Activity and Truck: ‹ label › to step, and a tap on the label for the choices
/// (Day, Week, Month, Custom; This year and All time are inside Custom). The choice is shared by the three screens and remembered.
struct PeriodControl: View {
    @Environment(AppState.self) private var state
    @State private var choosing = false

    var body: some View {
        let p = state.period
        let cal = Calendar.current
        ThemeStepper(
            title: p.title(calendar: cal), spokenTitle: p.spokenTitle(calendar: cal),
            canGoForward: !p.isCurrent(now: .now, calendar: cal), canStep: p.canStep, noun: PeriodSheetLogic.noun(p),
            previous: { state.setPeriod(p.shifted(by: -1, calendar: cal)) },
            next: { state.setPeriod(p.shifted(by: 1, calendar: cal)) },
            onTitle: { choosing = true })
        .sheet(isPresented: $choosing) { PeriodSheet().followsAppearance() }
    }
}

/// What the sheet's choices do, with no view code so a test can see them.
enum PeriodSheetLogic {
    /// Which of the four choices a period shows pressed: This year and All time live under Custom.
    enum Choice: String, CaseIterable, Identifiable {
        case day = "Day", week = "Week", month = "Month", custom = "Custom"
        var id: String { rawValue }
    }

    static func choice(_ p: Period) -> Choice {
        switch p.kind {
        case .day: return .day
        case .week: return .week
        case .month: return .month
        case .year, .all, .custom: return .custom
        }
    }

    /// Picking Day, Week or Month goes to the current one; picking Custom keeps the range in use (or the last 30 days).
    static func pick(_ c: Choice, from p: Period, now: Date, calendar: Calendar) -> Period {
        switch c {
        case .day: return Period(kind: .day, containing: now, calendar: calendar)
        case .week: return Period(kind: .week, containing: now, calendar: calendar)
        case .month: return Period(kind: .month, containing: now, calendar: calendar)
        case .custom:
            if choice(p) == .custom { return p }
            return last30(now: now, calendar: calendar)
        }
    }

    static func last30(now: Date, calendar: Calendar) -> Period {
        Period.custom(from: calendar.date(byAdding: .day, value: -29, to: now) ?? now, to: now, calendar: calendar)
    }

    static func thisYear(now: Date, calendar: Calendar) -> Period { Period(kind: .year, containing: now, calendar: calendar) }

    static func noun(_ p: Period) -> String {
        switch p.kind {
        case .day: return "day"
        case .week: return "week"
        case .year: return "year"
        default: return "month"
        }
    }
}

struct PeriodSheet: View {
    @Environment(AppState.self) private var state
    @Environment(\.dismiss) private var dismiss

    private var cal: Calendar { .current }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                    Picker("Period", selection: Binding(
                        get: { PeriodSheetLogic.choice(state.period) },
                        set: { state.setPeriod(PeriodSheetLogic.pick($0, from: state.period, now: .now, calendar: cal)) })) {
                        ForEach(PeriodSheetLogic.Choice.allCases) { Text($0.rawValue).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    if PeriodSheetLogic.choice(state.period) == .custom { custom }
                    Text(state.period.title(calendar: cal)).themeText(.value, color: Theme.textSecondary)
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle("Time scope")
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() }.tint(Theme.accent) } }
        }
        .presentationDetents([.medium, .large])
    }

    /// The two shortcuts, and the dates when dates are in use.
    @ViewBuilder
    private var custom: some View {
        let p = state.period
        let last = cal.date(byAdding: .day, value: -1, to: p.end) ?? p.end
        VStack(alignment: .leading, spacing: 3 * Theme.unit) {
            if p.kind == .custom {
                DatePicker("From", selection: Binding(get: { p.start }, set: { state.setPeriod(.custom(from: $0, to: last, calendar: cal)) }),
                           in: ...Date.now, displayedComponents: .date)
                DatePicker("To", selection: Binding(get: { last }, set: { state.setPeriod(.custom(from: p.start, to: $0, calendar: cal)) }),
                           in: p.start...Date.now, displayedComponents: .date)
            }
            HStack(spacing: 2 * Theme.unit) {
                shortcut("This year", on: p.kind == .year) { state.setPeriod(PeriodSheetLogic.thisYear(now: .now, calendar: cal)) }
                shortcut("All time", on: p.kind == .all) { state.setPeriod(Period.allTime(now: .now, calendar: cal)) }
                if p.kind == .year || p.kind == .all {
                    shortcut("Pick dates", on: false) { state.setPeriod(PeriodSheetLogic.last30(now: .now, calendar: cal)) }
                }
            }
        }
        .tint(Theme.accent)
    }

    private func shortcut(_ title: String, on: Bool, _ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title).themeText(.value, color: on ? Theme.accent : Theme.textPrimary)
                .frame(maxWidth: .infinity, minHeight: 44)
        }
        .buttonStyle(.plain)
        .background(on ? Theme.accent.opacity(0.15) : Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(on ? Theme.accent : Theme.border, lineWidth: 1))
        .accessibilityAddTraits(on ? .isSelected : [])
    }
}
