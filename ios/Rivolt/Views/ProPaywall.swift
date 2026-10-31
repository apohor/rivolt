import SwiftUI

/// Where an account stands on Pro, from the plan fields on `GET /api/auth/me`. MOCKED until the billing API merges; the web's twin is
/// `lib/plan.ts`. With no plan on the account (billing off, a self-hosted server) every screen here stays away.
enum PlanState: Equatable {
    case off
    case free
    case trial(ends: Date?)
    case pro(source: String, periodEnd: Date?)
    case lapsed(endedOn: Date?)
}

enum PlanLogic {
    static func state(_ me: AuthUser?, now: Date = .now) -> PlanState {
        guard let me, let plan = me.plan else { return .off }
        if plan == "pro" {
            if me.source == "trial" { return .trial(ends: me.trialEnds ?? me.periodEnd) }
            let known = ["apple", "stripe", "founding", "grant"]
            return .pro(source: known.contains(me.source ?? "") ? (me.source ?? "grant") : "grant", periodEnd: me.periodEnd)
        }
        // Free: a lapse when a paid period or a trial has ended; otherwise Pro was never had.
        let ended = [me.periodEnd, me.trialEnds].compactMap { $0 }.filter { $0 <= now }
        if let last = ended.max() { return .lapsed(endedOn: last) }
        return .free
    }

    static func date(_ d: Date?) -> String? {
        d?.formatted(.dateTime.year().month(.wide).day())
    }

    /// Whole days until `d`, never negative.
    static func daysLeft(_ d: Date?, now: Date = .now) -> Int? {
        d.map { max(0, Int(ceil($0.timeIntervalSince(now) / 86_400))) }
    }

    static func trialLeft(_ d: Date?, now: Date = .now) -> String? {
        guard let n = daysLeft(d, now: now) else { return nil }
        return n == 0 ? "ends today" : "\(n) \(n == 1 ? "day" : "days") left"
    }

    /// The App Store's own page for managing or cancelling a subscription (Apple 3.1.2, A6).
    static let manageSubscriptionsURL = URL(string: "https://apps.apple.com/account/subscriptions")!
}

/// Pro screen wording, in one place. PLACEHOLDER until growth's text lands; the prices are the plan's. The web's twin is `lib/proCopy.ts`.
enum ProCopy {
    static let title = "Rivolt Pro"
    static let subtitle = "Everything in Rivolt, for as long as you want it."
    static let monthly = (label: "Monthly", price: "$3.99", period: "month")
    static let yearly = (label: "Yearly", price: "$34.99", period: "year")
    static let included = [
        "Unlimited history in Now, Activity and Truck (free shows the last 90 days)",
        "Every month of Money, year views and off-peak savings over any period",
        "Trends, and range near a temperature",
        "Multi-day trips with hotel and campground overnights, re-plan from here, plan vs actual, unlimited saved trips",
        "Every vehicle shown",
        "Lock Screen and Home Screen widgets and the charging Live Activity",
    ]
    static let keepsFree = "Free keeps: recording, your full export, alerts, imports, and a single-day planner. Nothing is ever deleted when Pro ends."

    /// The trial and renewal terms shown on the paywall before the button (Apple 3.1.2): the trial's length, what it costs after, and
    /// that it renews automatically unless cancelled at least 24 hours before the end of the period.
    static func terms(yearly isYearly: Bool) -> String {
        let p = isYearly ? yearly : monthly
        return "Free for 14 days, then \(p.price) per \(p.period). The subscription renews automatically each \(p.period) unless you cancel at least 24 hours before the end of the current period. You can manage or cancel it any time in Settings → your name → Subscriptions on this iPhone."
    }

    static let restore = "Restore purchases"
    static let manage = "Manage subscription"
    static let unavailable = "Purchases aren't available yet. Nothing was charged."
    static let deletionNote = "If you subscribed through the App Store, cancel in Settings → your name → Subscriptions first: deleting your Rivolt account doesn't cancel it."
}

/// What buying and restoring do. The App Store (StoreKit 2) implementation is Pro 6; until then nothing can be bought and the
/// screen says so.
protocol ProPurchasing {
    func purchase(yearly: Bool) async throws
    func restore() async throws
}

struct UnavailablePurchasing: ProPurchasing {
    struct Unavailable: Error {}
    func purchase(yearly: Bool) async throws { throw Unavailable() }
    func restore() async throws { throw Unavailable() }
}

/// The paywall (A2 to A6): the title, the period and price of each choice and what is included before the button; the trial's length,
/// the price after and the renewal terms on the same screen; links to Terms and Privacy; Restore purchases; Manage subscription.
struct ProPaywallView: View {
    @Environment(AppState.self) private var state
    @Environment(\.openURL) private var openURL
    let plan: PlanState
    var purchasing: ProPurchasing = UnavailablePurchasing()

    @State private var yearly = true
    @State private var busy = false
    @State private var message: String?

    private var subscribedByApple: Bool {
        if case .pro(let source, _) = plan { return source == "apple" }
        return false
    }

    private var subscribed: Bool {
        if case .pro(let source, _) = plan { return source == "apple" || source == "stripe" }
        return false
    }

    var body: some View {
        SettingsStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                    Text(ProCopy.title).font(Theme.sans(ThemeTokens.typeHeadline, .semibold, relativeTo: .title3)).foregroundStyle(Theme.textPrimary)
                        .accessibilityAddTraits(.isHeader)
                    Text(ProCopy.subtitle).themeText(.value, color: Theme.textSecondary)
                    status
                    HStack(spacing: 8) {
                        choice(ProCopy.monthly.label, ProCopy.monthly.price, ProCopy.monthly.period, selected: !yearly) { yearly = false }
                        choice(ProCopy.yearly.label, ProCopy.yearly.price, ProCopy.yearly.period, selected: yearly) { yearly = true }
                    }
                    VStack(alignment: .leading, spacing: 6) {
                        Text("What's included").themeText(.label, color: Theme.textTertiary).accessibilityAddTraits(.isHeader)
                        ForEach(ProCopy.included, id: \.self) { Text("• \($0)").themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true) }
                        Text(ProCopy.keepsFree).themeText(.subline, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
                    }
                    if !subscribed {
                        Text(ProCopy.terms(yearly: yearly)).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                        Button { Task { await buy() } } label: { Text(trialButtonTitle).frame(maxWidth: .infinity) }
                            .buttonStyle(ThemeButtonStyle()).disabled(busy)
                    }
                    Button { Task { await restore() } } label: { Text(ProCopy.restore).frame(maxWidth: .infinity) }
                        .buttonStyle(ThemeSecondaryButtonStyle()).disabled(busy)
                    if subscribedByApple || subscribed {
                        Button { openURL(PlanLogic.manageSubscriptionsURL) } label: { Text(ProCopy.manage).frame(maxWidth: .infinity) }
                            .buttonStyle(ThemeSecondaryButtonStyle())
                    }
                    if let message { Text(message).themeText(.subline, color: Theme.estimate).accessibilityAddTraits(.updatesFrequently) }
                    HStack(spacing: 16) {
                        Link("Terms", destination: state.serverURL.appending(path: "terms"))
                        Link("Privacy", destination: state.serverURL.appending(path: "privacy"))
                    }
                    .themeText(.subline, color: Theme.accent)
                    Text("Unofficial, not affiliated with Rivian.").themeText(.caption, color: Theme.textTertiary)
                }
                .padding(4 * Theme.unit)
            }
            .themePage()
            .navigationTitle(ProCopy.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(Theme.bar, for: .navigationBar)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar { ToolbarItem(placement: .confirmationAction) { SheetDoneButton() } }
        }
    }

    private var trialButtonTitle: String {
        if case .trial = plan { return "Subscribe" }
        if case .lapsed = plan { return "Subscribe" }
        return "Start 14-day free trial"
    }

    @ViewBuilder private var status: some View {
        switch plan {
        case .trial(let ends):
            Text("Your trial: \(PlanLogic.trialLeft(ends) ?? "running")\(PlanLogic.date(ends).map { " (until \($0))" } ?? "").")
                .themeText(.subline, color: Theme.textSecondary)
        case .pro(let source, let end):
            Text(source == "founding" ? "Pro is on us\(PlanLogic.date(end).map { " until \($0)" } ?? ""). No card, and nothing charges you."
                 : "You're subscribed\(PlanLogic.date(end).map { "; the current period ends \($0)" } ?? "").")
                .themeText(.subline, color: Theme.textSecondary)
        case .lapsed(let on):
            Text("Pro ended\(PlanLogic.date(on).map { " on \($0)" } ?? ""). You're on the free window; everything older is kept, stays in your export, and comes back if you subscribe.")
                .themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
        default:
            EmptyView()
        }
    }

    private func choice(_ label: String, _ price: String, _ period: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 2) {
                Text(label).themeText(.subline, color: Theme.textSecondary)
                Text("\(price) / \(period)").themeText(.value, color: Theme.textPrimary).fontWeight(.semibold)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(3 * Theme.unit)
            .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(selected ? Theme.accent : Theme.border, lineWidth: selected ? 2 : 1))
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityLabel("\(label), \(price) per \(period)")
    }

    private func buy() async {
        busy = true
        message = nil
        defer { busy = false }
        do { try await purchasing.purchase(yearly: yearly) } catch { message = ProCopy.unavailable }
    }

    private func restore() async {
        busy = true
        message = nil
        defer { busy = false }
        do { try await purchasing.restore() } catch { message = ProCopy.unavailable }
    }
}

/// The lapse notice at the top of Now: Pro ended, the free window applies, nothing is deleted, the export stays free. Only for a
/// lapsed account on a server that bills; one dismissal per lapse.
struct PlanNoticeView: View {
    @Environment(AppState.self) private var state
    @AppStorage("rivolt.planNotice.dismissed") private var dismissedOn = ""
    @State private var plan: PlanState = .off
    @State private var showing = false

    private var lapseID: String? {
        if case .lapsed(let on) = plan { return on.map { ISO8601DateFormatter().string(from: $0) } ?? "lapsed" }
        return nil
    }

    var body: some View {
        Group {
            if let id = lapseID, dismissedOn != id, case .lapsed(let on) = plan {
                VStack(alignment: .leading, spacing: 8) {
                    Text("Pro ended\(PlanLogic.date(on).map { " on \($0)" } ?? ""). Now and Activity show the last 90 days. Nothing is deleted, and you can export everything any time.")
                        .themeText(.subline, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
                    HStack(spacing: 12) {
                        Button("See Pro") { showing = true }.buttonStyle(ThemeButtonStyle())
                        Button("Dismiss") { dismissedOn = id }.buttonStyle(ThemeSecondaryButtonStyle())
                    }
                }
                .padding(4 * Theme.unit)
                .frame(maxWidth: .infinity, alignment: .leading)
                .themeCard(padding: 0)
                .accessibilityElement(children: .contain)
                .sheet(isPresented: $showing) { ProPaywallView(plan: plan).followsAppearance() }
            }
        }
        .task {
            guard !state.isDemo else { return }
            plan = PlanLogic.state(try? await state.client.me())
        }
    }
}
