import SwiftUI

/// Where an account stands on Pro, from the plan fields on `GET /api/auth/me`. MOCKED until the billing API merges; the web's twin is
/// `lib/plan.ts`. With no plan on the account (billing off, a self-hosted server) every screen here stays away.
enum PlanState: Equatable {
    case off
    case free
    case trial(ends: Date?)
    case pro(source: String, periodEnd: Date?)
    /// A paid subscription set to not renew: Pro stays on until `until`.
    case cancelled(source: String, until: Date?)
    /// A renewal payment failed: Pro stays on until `until` (the 7-day grace) while the payment is retried.
    case paymentFailed(source: String, until: Date?)
    case lapsed(endedOn: Date?, source: String?)
}

enum PlanLogic {
    /// Billing off (a self-hosted server, `billingEnabled` false or absent), the demo account, or no plan fields at all mean no plan screen.
    static func state(_ me: AuthUser?) -> PlanState {
        guard let me, let plan = me.plan, me.billingEnabled == true else { return .off }
        if me.planSource == "demo" || me.planSource == "billing_off" { return .off }
        if plan == "pro" {
            if me.planSource == "trial" { return .trial(ends: me.planTrialEnds ?? me.planPeriodEnd) }
            let known = ["apple", "stripe", "founding", "grant"]
            let source = known.contains(me.planSource ?? "") ? (me.planSource ?? "grant") : "grant"
            // A failed payment first (it needs the person), then a cancellation: a cancelled plan must never say it renews.
            if source == "stripe", let g = me.planGraceEnds { return .paymentFailed(source: source, until: g) }
            if source == "stripe" || source == "apple", let c = me.planCancelsAt { return .cancelled(source: source, until: c) }
            return .pro(source: source, periodEnd: me.planPeriodEnd)
        }
        if let ended = me.planEndedAt { return .lapsed(endedOn: ended, source: me.planEndedSource) }
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

    /// What a free account's Money shows (backend #1370): this month and the two before it.
    static let freeMonths = 3

    /// Whole calendar months from `month` ("2026-07") up to `now` ("2026-10"): 3 for July when it is October; 0 for a malformed month.
    static func monthsBack(_ month: String, now: String) -> Int {
        func parts(_ s: String) -> (Int, Int)? {
            let p = s.split(separator: "-")
            guard p.count == 2, let y = Int(p[0]), let m = Int(p[1]), (1...12).contains(m) else { return nil }
            return (y, m)
        }
        guard let a = parts(month), let b = parts(now) else { return 0 }
        return (b.0 - a.0) * 12 + (b.1 - a.1)
    }

    /// True when this plan shows only the last `freeMonths` months and the month is older: its CSV is part of Pro. Pro, billing off and the
    /// demo (`.off`) see everything.
    static func csvIsPro(_ plan: PlanState, month: String, now: String) -> Bool {
        switch plan {
        case .free, .lapsed: return monthsBack(month, now: now) >= freeMonths
        default: return false
        }
    }

    /// The App Store's own page for managing or cancelling a subscription (Apple 3.1.2, A6).
    static let manageSubscriptionsURL = URL(string: "https://apps.apple.com/account/subscriptions")!
}

/// Pro screen wording, from growth's docs/PRO_LAUNCH_COPY.md section 10 (PR #1366; DA's copy check is part of that PR, so re-verify
/// this against the merged text before billing is on: LAUNCH-BLOCKING). The prices are the plan's. The web's twin is `lib/proCopy.ts`.
enum ProCopy {
    static let title = "Rivolt Pro"
    static let subtitle = "Full history, multi-day trips and widgets, from $3.99 a month."
    static let included = [
        "Unlimited history and trends (free: the last 90 days)",
        "Range near a temperature",
        "Multi-day trip plans with hotel and campground overnights",
        "Re-plan from where the truck is, and plan-vs-actual after the trip",
        "Unlimited saved trips (free: 3)",
        "Every vehicle shown (free: 1)",
        "iPhone: Lock Screen and Home Screen widgets, the charging Live Activity",
        "Always free, on every plan: recording, every alert, imports, share cards, the full export, deletion",
    ]
    static let yearly = (label: "Yearly", price: "$34.99", unit: "a year", extra: "about $2.92 a month")
    static let monthly = (label: "Monthly", price: "$3.99", unit: "a month", extra: "")

    /// The trial and renewal terms shown on the paywall before the button (Apple 3.1.2): the trial's length, what it costs after, that
    /// it renews automatically, how long before the end to cancel and where, and who is charged.
    static let terms = "14 days free, then $34.99 a year (or $3.99 a month). Renews automatically until cancelled. Cancel at least 24 hours before the end of the trial or any period in Settings → Apple ID → Subscriptions. Payment is charged to your Apple ID."
    static let underButton = "Payment is charged to your Apple ID. Stop paying and nothing is deleted."
    static let restore = "Restore purchases"
    static let restoreHelp = "Bought Pro on this Apple ID before? Restore it here. A subscription bought on rivolt.dev is already on your account; sign in and it applies."
    static let manage = "Manage subscription"
    static let manageHelp = "Opens your Apple subscriptions, where you change or cancel the plan. Cancelling keeps Pro until the paid period ends."
    static let unofficial = "Unofficial, not affiliated with Rivian."
    static let unavailable = "Purchases aren't available yet. Nothing was charged."
    static let deletionNote = "If you subscribed through the App Store, cancel in Settings → your name → Subscriptions first: deleting your Rivolt account doesn't cancel it."

    static func choiceLabel(_ yearly: Bool) -> String { yearly ? "Subscribe: $34.99 a year, renews yearly" : "Subscribe: $3.99 a month, renews monthly" }

    /// The Settings plan card, one state at a time (the web's `planCardCopy`).
    static func card(_ state: PlanState, now: Date = .now) -> (title: String, line: String)? {
        switch state {
        case .off:
            return nil
        case .free:
            return ("Free", "Recording, alerts, the last 90 days and the full export, always.")
        case .trial(let ends):
            let left = PlanLogic.trialLeft(ends, now: now).map { "\($0.prefix(1).uppercased())\($0.dropFirst()). " } ?? ""
            return ("Pro trial, ends \(PlanLogic.date(ends) ?? "soon")", "\(left)No card, nothing is charged. Afterwards the free tier keeps every recording and the full export.")
        case .pro(let source, let end):
            let renews = PlanLogic.date(end).map { ", renews \($0)" } ?? ""
            switch source {
            case "apple": return ("Rivolt Pro\(renews)", "Billed through Apple.")
            case "stripe": return ("Rivolt Pro\(renews)", "Billed through Stripe.")
            case "founding":
                return ("Rivolt Pro, free until \(PlanLogic.date(end) ?? "the end of the year")", "Yours at no charge for the first year; no card, nothing is charged. We'll email you 30 days before it ends.")
            default: return ("Rivolt Pro", "Granted to this account.")
            }
        case .cancelled(_, let until):
            return ("Rivolt Pro until \(PlanLogic.date(until) ?? "the end of the period")", "Cancelled; Pro stays on until then. Nothing is deleted after.")
        case .paymentFailed(_, let until):
            return ("Rivolt Pro, payment failed", "Update your card by \(PlanLogic.date(until) ?? "the end of the week") to keep Pro. Nothing is deleted either way.")
        case .lapsed(let on, _):
            return ("Your Pro ended\(PlanLogic.date(on).map { " on \($0)" } ?? "")", "Showing the last 90 days. Nothing is deleted; older history stays stored and is in your export any time (Settings → Data).")
        }
    }

    /// The trial line for someone whose trial was the web's (no card), shown only while one runs or after it ended.
    static func trialLine(_ state: PlanState) -> String? {
        switch state {
        case .trial(let ends):
            return "You have 14 days of Pro free, with no card. It ends\(PlanLogic.date(ends).map { " on \($0)" } ?? "") on its own; nothing is charged unless you subscribe."
        case .lapsed(_, let source) where source == "trial":
            return "14 days of Pro were free when you signed up. Subscribe to keep it; cancel any time."
        default:
            return nil
        }
    }
}

/// A free account's limits answer HTTP 402 with a code (never 403, which the app reads as signed-out or terms-required): act on the code.
/// Bodies: Fixtures/err_outside_free_window.json and err_pro_required.json. The client surfaces them as `.server(status: 402, message: code)`.
enum ProLimit: Equatable {
    case outsideFreeWindow
    case proRequired

    static func of(_ error: Error) -> ProLimit? {
        guard case APIError.server(let status, let message) = error, status == 402 else { return nil }
        switch message {
        case "outside_free_window": return .outsideFreeWindow
        case "pro_required": return .proRequired
        default: return nil
        }
    }

    func text(freeDays: Int = 90) -> String {
        switch self {
        case .outsideFreeWindow: return "This is older than the last \(freeDays) days that the free plan shows. It is kept, and it is in your export; Pro shows all of it."
        case .proRequired: return "This is part of Rivolt Pro. Everything you recorded is kept either way."
        }
    }
}

/// What a screen shows in place of an error when the server answers a free account's limit (HTTP 402): why, that nothing is lost, and,
/// where the server bills, the way to Pro.
struct ProLimitNote: View {
    @Environment(AppState.self) private var state
    let limit: ProLimit
    var freeDays: Int = 90
    @State private var plan: PlanState = .off
    @State private var showing = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(limit.text(freeDays: freeDays)).themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
            if plan != .off {
                Button("See Pro") { showing = true }.buttonStyle(ThemeSecondaryButtonStyle())
            }
        }
        .padding(4 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
        .themeCard(padding: 0)
        .accessibilityElement(children: .contain)
        .task {
            guard !state.isDemo else { return }
            plan = PlanLogic.state(try? await state.client.me())
        }
        .sheet(isPresented: $showing) { ProPaywallView(plan: plan).followsAppearance() }
    }
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

/// The paywall (A2 to A6): the title, the period and price of each choice (nothing pre-selected) and what is included before the button;
/// the trial's length, the price after and the renewal terms on the same screen; Terms and Privacy under the button; Restore purchases;
/// Manage subscription.
struct ProPaywallView: View {
    @Environment(AppState.self) private var state
    @Environment(\.openURL) private var openURL
    let plan: PlanState
    var purchasing: ProPurchasing = UnavailablePurchasing()

    @State private var yearly: Bool?
    @State private var busy = false
    @State private var message: String?

    private var subscribed: Bool {
        switch plan {
        case .pro(let source, _), .cancelled(let source, _), .paymentFailed(let source, _): return source == "apple" || source == "stripe"
        default: return false
        }
    }

    var body: some View {
        SettingsStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 4 * Theme.unit) {
                    Text(ProCopy.title).font(Theme.sans(ThemeTokens.typeHeadline, .semibold, relativeTo: .title3)).foregroundStyle(Theme.textPrimary)
                        .accessibilityAddTraits(.isHeader)
                    Text(ProCopy.subtitle).themeText(.value, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                    status
                    VStack(alignment: .leading, spacing: 6) {
                        Text("What's included").themeText(.label, color: Theme.textTertiary).accessibilityAddTraits(.isHeader)
                        ForEach(ProCopy.included, id: \.self) { Text("• \($0)").themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true) }
                    }
                    if !subscribed {
                        HStack(spacing: 8) {
                            choice(ProCopy.yearly.label, ProCopy.yearly.price, ProCopy.yearly.unit, ProCopy.yearly.extra, selected: yearly == true) { yearly = true }
                            choice(ProCopy.monthly.label, ProCopy.monthly.price, ProCopy.monthly.unit, ProCopy.monthly.extra, selected: yearly == false) { yearly = false }
                        }
                        Text(ProCopy.terms).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                        Button { Task { await buy() } } label: { Text(yearly.map(ProCopy.choiceLabel) ?? "Choose a plan").frame(maxWidth: .infinity) }
                            .buttonStyle(ThemeButtonStyle()).disabled(busy || yearly == nil)
                        HStack(spacing: 8) {
                            Link("Terms", destination: state.serverURL.appending(path: "terms"))
                            Text("·").foregroundStyle(Theme.textTertiary)
                            Link("Privacy", destination: state.serverURL.appending(path: "privacy"))
                        }
                        .themeText(.subline, color: Theme.accent)
                        Text(ProCopy.underButton).themeText(.subline, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
                    }
                    Button { Task { await restore() } } label: { Text(ProCopy.restore).frame(maxWidth: .infinity) }
                        .buttonStyle(ThemeSecondaryButtonStyle()).disabled(busy)
                    Text(ProCopy.restoreHelp).themeText(.subline, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
                    if subscribed {
                        Button { openURL(PlanLogic.manageSubscriptionsURL) } label: { Text(ProCopy.manage).frame(maxWidth: .infinity) }
                            .buttonStyle(ThemeSecondaryButtonStyle())
                        Text(ProCopy.manageHelp).themeText(.subline, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
                    }
                    if let message { Text(message).themeText(.subline, color: Theme.estimate).accessibilityAddTraits(.updatesFrequently) }
                    if subscribed {
                        HStack(spacing: 8) {
                            Link("Terms", destination: state.serverURL.appending(path: "terms"))
                            Text("·").foregroundStyle(Theme.textTertiary)
                            Link("Privacy", destination: state.serverURL.appending(path: "privacy"))
                        }
                        .themeText(.subline, color: Theme.accent)
                    }
                    Text(ProCopy.unofficial).themeText(.caption, color: Theme.textTertiary)
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

    @ViewBuilder private var status: some View {
        if let card = ProCopy.card(plan) {
            VStack(alignment: .leading, spacing: 2) {
                Text(card.title).themeText(.value, color: Theme.textPrimary).fontWeight(.semibold)
                Text(card.line).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                if let trial = ProCopy.trialLine(plan) {
                    Text(trial).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    private func choice(_ label: String, _ price: String, _ unit: String, _ extra: String, selected: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 2) {
                Text(label).themeText(.subline, color: Theme.textSecondary)
                Text("\(price) \(unit)").themeText(.value, color: Theme.textPrimary).fontWeight(.semibold)
                if !extra.isEmpty { Text(extra).themeText(.caption, color: Theme.textTertiary) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(3 * Theme.unit)
            .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(selected ? Theme.accent : Theme.border, lineWidth: selected ? 2 : 1))
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityLabel("\(label), \(price) \(unit)")
    }

    private func buy() async {
        guard let yearly else { return }
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

/// The lapse banner at the top of Now: Pro ended, the free window applies, nothing is deleted, the export stays free. Only for a lapsed
/// account on a server that bills; one dismissal per lapse.
struct PlanNoticeView: View {
    @Environment(AppState.self) private var state
    @AppStorage("rivolt.planNotice.dismissed") private var dismissedOn = ""
    @State private var plan: PlanState = .off
    @State private var showing = false

    private var lapseID: String? {
        if case .lapsed(let on, _) = plan { return on.map { ISO8601DateFormatter().string(from: $0) } ?? "lapsed" }
        return nil
    }

    var body: some View {
        Group {
            if let id = lapseID, dismissedOn != id, let card = ProCopy.card(plan) {
                VStack(alignment: .leading, spacing: 8) {
                    Text("\(card.title). \(card.line)")
                        .themeText(.subline, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
                    HStack(spacing: 12) {
                        Button("Keep Pro") { showing = true }.buttonStyle(ThemeButtonStyle())
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
