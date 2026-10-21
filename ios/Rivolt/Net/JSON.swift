import Foundation

/// The decoder every API response goes through. Go encodes `time.Time` as
/// RFC 3339 with fractional seconds whenever they're non-zero
/// ("2026-10-21T14:03:12.123456789Z"), which `.iso8601` rejects, so dates
/// accept both forms.
enum JSON {
    static func decoder() -> JSONDecoder {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        d.dateDecodingStrategy = .custom { decoder in
            let raw = try decoder.singleValueContainer().decode(String.self)
            if let date = parseDate(raw) { return date }
            throw DecodingError.dataCorrupted(.init(
                codingPath: decoder.codingPath,
                debugDescription: "not an RFC 3339 date: \(raw)"))
        }
        return d
    }

    /// RFC 3339, with or without fractional seconds. Go may send up to nine
    /// fractional digits; ISO8601DateFormatter reads at most milliseconds, so
    /// longer fractions are cut to three first.
    static func parseDate(_ raw: String) -> Date? {
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        if let d = plain.date(from: raw) { return d }
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return fractional.date(from: trimFraction(raw))
    }

    private static func trimFraction(_ raw: String) -> String {
        guard let dot = raw.firstIndex(of: ".") else { return raw }
        let digitsStart = raw.index(after: dot)
        var end = digitsStart
        while end < raw.endIndex, raw[end].isNumber { end = raw.index(after: end) }
        let digits = raw[digitsStart..<end]
        guard digits.count > 3 else { return raw }
        return String(raw[..<digitsStart]) + digits.prefix(3) + raw[end...]
    }
}
