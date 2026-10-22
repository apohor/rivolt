import Foundation

/// The map's look: the bundled light and dark styles (ios/design/generate_map_style.mjs, from the web's
/// @protomaps/basemaps flavors), with the signed-in server's tile archive put into their one network URL. Everything
/// else a style refers to is in the app bundle (asset://), so a map makes no request to anyone but that server.
enum MapStyle {
    static let serverPlaceholder = "__RIVOLT_SERVER__"

    /// The style JSON for `dark` or light, pointing at `server`'s `/api/maps/tiles/us.pmtiles`; nil when the bundled
    /// file is missing.
    static func json(dark: Bool, server: URL, bundle: Bundle = .main) -> String? {
        guard let url = bundle.url(forResource: dark ? "style-dark" : "style-light", withExtension: "json", subdirectory: "Map"),
              let raw = try? String(contentsOf: url, encoding: .utf8) else { return nil }
        return raw.replacingOccurrences(of: serverPlaceholder, with: origin(server))
    }

    /// "https://rivolt.dev" (scheme, host and a non-default port; never a path or credentials).
    static func origin(_ server: URL) -> String {
        var c = URLComponents()
        c.scheme = server.scheme
        c.host = server.host
        c.port = server.port
        return c.string ?? ""
    }

    /// Every URL-like string in a style (sources, tiles, glyphs, sprite), for the check that they are all the server's
    /// or the bundle's.
    static func urls(in styleJSON: String) -> [String] {
        let pattern = try! NSRegularExpression(pattern: #""([a-zA-Z][a-zA-Z0-9+.-]*://[^"]*)""#)
        let ns = styleJSON as NSString
        return pattern.matches(in: styleJSON, range: NSRange(location: 0, length: ns.length)).map { ns.substring(with: $0.range(at: 1)) }
    }

    /// Whether `url` is allowed in a style: in the bundle (asset://), or the server's tile archive (directly or through
    /// pmtiles://). Nothing else: no third-party host, no mapbox:// or maptiler shorthand.
    static func isAllowed(_ url: String, server: URL) -> Bool {
        if url.hasPrefix("asset://") { return true }
        let inner = url.hasPrefix("pmtiles://") ? String(url.dropFirst("pmtiles://".count)) : url
        guard let u = URL(string: inner), u.user == nil, u.password == nil else { return false }
        return origin(u) == origin(server)
    }
}
