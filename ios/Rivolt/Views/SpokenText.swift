import Foundation

/// Words for what the screens print in short form, so VoiceOver reads "9.8 miles. 1 drive, since Oct 19" and not "mi",
/// "·" or "~": units spelled out, "·" a pause, "~" "about", arrows "to", signs and percent as words. The figures and
/// names themselves are left as they are.
enum SpokenText {
    private static let rules: [(pattern: String, template: String)] = [
        // Compound units first, so their parts aren't spelled out on their own.
        (#"\bmi/kWh\b"#, "miles per kilowatt hour"),
        (#"\bkm/kWh\b"#, "kilometres per kilowatt hour"),
        (#"\bmi/%"#, "miles per percent"),
        (#"\bkm/%"#, "kilometres per percent"),
        (#"\b([A-Z]{3})/kWh\b"#, "$1 per kilowatt hour"),
        (#"\b([A-Z]{3})/mi\b"#, "$1 per mile"),
        (#"\b([A-Z]{3})/km\b"#, "$1 per kilometre"),
        (#"\bmph\b"#, "miles per hour"),
        (#"\bkm/h\b"#, "kilometres per hour"),
        (#"\bkWh\b"#, "kilowatt hours"),
        (#"\bkW\b"#, "kilowatts"),
        (#"(\d)\s?mi\b"#, "$1 miles"),
        (#"(\d)\s?km\b"#, "$1 kilometres"),
        // Durations as "1h 5m", "5 h 6 m" and "26 min".
        (#"(\d)\s?h\b"#, "$1 hours"),
        (#"(\d)\s?m\b"#, "$1 minutes"),
        (#"(\d)\s?min\b"#, "$1 minutes"),
        (#"\best\."#, "estimate"),
        // Symbols.
        (#"\s?→\s?"#, " to "),
        (#"~"#, "about "),
        (#"[−\-](?=\d)"#, "minus "),
        (#"\+(?=\d)"#, "plus "),
        (#"(\d)\s?%"#, "$1 percent"),
        (#"—"#, "no figure"),
        (#"\s·\s"#, ", "),
        (#"·"#, ","),
    ]

    static func words(_ s: String) -> String {
        var out = s
        for rule in rules {
            guard let re = try? NSRegularExpression(pattern: rule.pattern) else { continue }
            out = re.stringByReplacingMatches(in: out, range: NSRange(out.startIndex..., in: out), withTemplate: rule.template)
        }
        return out.replacingOccurrences(of: "  ", with: " ")
    }
}
