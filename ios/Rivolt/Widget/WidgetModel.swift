import Foundation
import WidgetKit

/// What the home-screen and lock-screen widgets show, and the only truck data
/// kept between refreshes (in the App Group, so a failed refresh can show the
/// last values with their age). A typed struct of exactly these fields: no VIN,
/// no name, no place, no coordinates.
struct WidgetSnapshot: Codable, Equatable {
    enum Attention: String, Codable {
        case none, needsReconnect, paused
    }

    enum Freshness: Equatable {
        case fresh, stale, unknown
    }

    enum Tone: Equatable {
        case normal, charging, stale
    }

    var socPct: Double
    var rangeKm: Double
    var stateLabel: String
    var isCharging: Bool
    var chargePowerKw: Double?
    var chargeTargetPct: Double?
    var lastSeen: Date?
    /// How old the data may be before it is called out of date, for the state it was taken in.
    var cadenceSeconds: TimeInterval
    var attention: Attention

    /// From what the widget fetched. Nil without a charge level and a range: nothing sensible to show.
    static func make(input: NowSummary.Input?, now: NowSnapshot?, clock: Date) -> WidgetSnapshot? {
        let summary = NowSummary(input: input, now: now, clock: clock)
        guard let soc = summary.socPct, let range = summary.rangeKm else { return nil }
        let kind = now?.state ?? NowSummary.inferState(from: input)
        let attention: Attention
        switch summary.freshness {
        case .needsReconnect: attention = .needsReconnect
        case .paused: attention = .paused
        default: attention = .none
        }
        return WidgetSnapshot(
            socPct: soc, rangeKm: range, stateLabel: summary.stateLabel, isCharging: kind == .charging,
            chargePowerKw: summary.chargePowerKw, chargeTargetPct: summary.chargeTargetPct,
            lastSeen: summary.lastSeen, cadenceSeconds: NowSummary.cadence(kind), attention: attention)
    }

    /// Fresh, stale or unknown as of `date` (an entry's date, not "now": the
    /// timeline holds a later entry that flips to stale on time).
    func freshness(at date: Date) -> Freshness {
        guard let seen = lastSeen else { return .unknown }
        return date.timeIntervalSince(seen) <= cadenceSeconds ? .fresh : .stale
    }

    func isStale(at date: Date) -> Bool { attention == .none && freshness(at: date) == .stale }

    /// "Updated 3 hr. ago".
    func ageText(at date: Date) -> String? {
        guard let seen = lastSeen else { return nil }
        let f = RelativeDateTimeFormatter()
        f.unitsStyle = .abbreviated
        return "Updated \(f.localizedString(for: seen, relativeTo: date))"
    }

    /// The one status line: what the truck is doing, or why the numbers can't be trusted.
    func status(at date: Date) -> (text: String, icon: String, tone: Tone) {
        switch attention {
        case .needsReconnect: return ("Reconnect on the web", "exclamationmark.triangle", .stale)
        case .paused: return ("Recording paused", "pause.circle", .normal)
        case .none: break
        }
        if isStale(at: date) {
            return (ageText(at: date) ?? "May be out of date", "clock.badge.exclamationmark", .stale)
        }
        var text = stateLabel
        if let kw = chargePowerKw { text += " · \(NumberText.fixed(kw, 1)) kW" }
        return (text, isCharging ? "bolt.fill" : "parkingsign", isCharging ? .charging : .normal)
    }

    static let sample = WidgetSnapshot(
        socPct: 63, rangeKm: 312, stateLabel: "Charging", isCharging: true, chargePowerKw: 11.1,
        chargeTargetPct: 80, lastSeen: nil, cadenceSeconds: 600, attention: .none)
}

enum WidgetContent: Equatable {
    case data(WidgetSnapshot)
    /// No usable session: never signed in, signed out, or the server refused the token or the truck.
    case signedOut
    /// Signed in but nothing to show yet.
    case unavailable
}

struct NowEntry: TimelineEntry {
    let date: Date
    let content: WidgetContent
}

/// Entries and refresh times. The system decides how often a widget really
/// refreshes (a daily budget); these are what the widget asks for.
enum WidgetTimelineBuilder {
    static func entries(_ content: WidgetContent, at now: Date) -> [NowEntry] {
        var out = [NowEntry(date: now, content: content)]
        // A second entry at the moment the data turns stale, so "Updated 3 hr. ago" appears on time without a refresh.
        if case .data(let s) = content, s.attention == .none, let seen = s.lastSeen {
            let flip = seen.addingTimeInterval(s.cadenceSeconds + 1)
            if flip > now, flip.timeIntervalSince(now) <= 6 * 3600 {
                out.append(NowEntry(date: flip, content: content))
            }
        }
        return out
    }

    static func nextRefresh(_ content: WidgetContent, at now: Date) -> Date {
        let minutes: Double
        switch content {
        case .data(let s):
            if s.isStale(at: now) || s.attention != .none {
                minutes = 60
            } else if s.isCharging || s.stateLabel == "Driving" {
                minutes = 15
            } else if s.stateLabel == "Asleep" {
                minutes = 60
            } else {
                minutes = 30
            }
        case .signedOut, .unavailable:
            minutes = 60
        }
        return now.addingTimeInterval(minutes * 60)
    }
}

/// The App Group's small store: which truck and server the widget reads, and
/// the last snapshot. Never the token (that is in the Keychain group). Cleared
/// on sign-out, on a server switch, on account deletion and whenever the server
/// refuses the widget (401, 403 or 404), so a revoked session or a lost truck
/// never keeps showing old numbers.
struct WidgetStore {
    static let groupID = "group.dev.rivolt.app"
    private static let vehicleKey = "widget.vehicle_id"
    private static let serverKey = "widget.server"
    private static let snapshotKey = "widget.snapshot"

    let defaults: UserDefaults

    init(defaults: UserDefaults? = UserDefaults(suiteName: WidgetStore.groupID)) {
        self.defaults = defaults ?? .standard
    }

    var vehicleID: String? {
        defaults.string(forKey: Self.vehicleKey).flatMap { WidgetFetch.validID($0) ? $0 : nil }
    }

    var server: URL? {
        defaults.string(forKey: Self.serverKey).flatMap { try? ServerURL.normalize($0) }
    }

    var snapshot: WidgetSnapshot? {
        defaults.data(forKey: Self.snapshotKey).flatMap { try? JSONDecoder().decode(WidgetSnapshot.self, from: $0) }
    }

    /// The app tells the widget which truck and server the signed-in person is using.
    func setSession(vehicleID: String, server: URL) {
        guard WidgetFetch.validID(vehicleID) else { return }
        // A different server or truck must not show the old one's numbers.
        if self.server?.absoluteString != server.absoluteString || self.vehicleID != vehicleID { defaults.removeObject(forKey: Self.snapshotKey) }
        defaults.set(vehicleID, forKey: Self.vehicleKey)
        defaults.set(ServerURL.origin(of: server), forKey: Self.serverKey)
    }

    func save(_ snapshot: WidgetSnapshot) {
        if let data = try? JSONEncoder().encode(snapshot) { defaults.set(data, forKey: Self.snapshotKey) }
    }

    func clear() {
        for k in [Self.vehicleKey, Self.serverKey, Self.snapshotKey] { defaults.removeObject(forKey: k) }
    }
}

/// What a fetch result becomes on screen, with the store updated to match.
enum WidgetResolver {
    static func resolve(_ outcome: WidgetFetch.Outcome, store: WidgetStore) -> WidgetContent {
        switch outcome {
        case .snapshot(let s):
            store.save(s)
            return .data(s)
        case .signedOut, .revoked:
            store.clear()
            return .signedOut
        case .failed:
            // Keep what was last known, with its age; never a fresh-looking empty.
            if let last = store.snapshot { return .data(last) }
            return .unavailable
        }
    }
}
