import Foundation

/// The "Your Rivian connection expires Nov 3." notice on Now, as drafted in N49 (rivolt-infra #203): from 7 days before, one tone,
/// with a Reconnect link to Settings → Rivian account on the web (the Rivian sign-in is done there). It shows only when nothing worse
/// applies and the server sent the field; with an older server there is nothing to show.
enum RivianExpiryLogic {
    static let bannerDays = 7

    /// The line to show, or nil.
    static func notice(_ s: RivianStatus?, locale: Locale = .current, timeZone: TimeZone = .current) -> String? {
        guard let s, s.enabled != false, s.authenticated == true, s.paused != true, s.mfaPending != true, s.needsReauth != true,
              let days = s.sessionDaysLeft, days <= bannerDays else { return nil }
        if let date = shortDate(s.sessionExpiresAt, locale: locale, timeZone: timeZone) { return "Your Rivian connection expires \(date)." }
        return days <= 0 ? "Your Rivian connection expires today."
            : days == 1 ? "Your Rivian connection expires in 1 day."
            : "Your Rivian connection expires in \(days) days."
    }

    /// "Nov 3" in the reader's language and zone; nil when missing or unreadable.
    static func shortDate(_ iso: String?, locale: Locale = .current, timeZone: TimeZone = .current) -> String? {
        guard let iso, !iso.isEmpty else { return nil }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        let g = ISO8601DateFormatter()
        g.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        guard let date = f.date(from: iso) ?? g.date(from: iso) else { return nil }
        let out = DateFormatter()
        out.locale = locale
        out.timeZone = timeZone
        out.setLocalizedDateFormatFromTemplate("MMMd")
        return out.string(from: date)
    }

    /// Where Reconnect goes: the web's Rivian account settings.
    static let reconnectPath = "/settings?tab=account"
}
