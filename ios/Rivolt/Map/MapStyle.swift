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

    static let chargerSourceID = "rivolt-chargers"
    static let chargerProbeLayerID = "rivolt-chargers-probe"

    /// `style` with the server's charger archive (`/api/maps/tiles/chargers.pmtiles`: NREL AFDC stations and
    /// Recreation.gov campgrounds) as a source, and a near-invisible layer on it so MapLibre loads its tiles for the
    /// nearest-charger lookup (a layer MapLibre wouldn't draw at all would leave the source unloaded). Nil when
    /// `style` isn't a JSON object.
    static func withChargers(_ style: String, server: URL) -> String? {
        guard var root = (try? JSONSerialization.jsonObject(with: Data(style.utf8))) as? [String: Any],
              var sources = root["sources"] as? [String: Any],
              var layers = root["layers"] as? [[String: Any]] else { return nil }
        sources[chargerSourceID] = ["type": "vector", "url": "pmtiles://\(origin(server))/api/maps/tiles/chargers.pmtiles"]
        layers.append([
            "id": chargerProbeLayerID, "type": "circle", "source": chargerSourceID, "source-layer": "chargers",
            "paint": ["circle-opacity": 0.01, "circle-radius": 0.5],
        ])
        root["sources"] = sources
        root["layers"] = layers
        guard let data = try? JSONSerialization.data(withJSONObject: root, options: [.withoutEscapingSlashes]) else { return nil }
        return String(decoding: data, as: UTF8.self)
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

/// The characters the map can draw: the glyph ranges the app ships (Map/glyphs/Noto Sans Medium, 256 characters a file). MapLibre draws no part of a
/// label with one character outside them, so a name from the data (a hotel's, a charger's) keeps only the characters it can draw.
enum MapGlyphs {
    static let ranges: Set<Int> = Set((Bundle.main.urls(forResourcesWithExtension: "pbf", subdirectory: "Map/glyphs/Noto Sans Medium") ?? []).compactMap {
        $0.deletingPathExtension().lastPathComponent.split(separator: "-").first.flatMap { Int($0) }
    })

    static func canDraw(_ c: Unicode.Scalar) -> Bool { ranges.contains(Int(c.value) / 256 * 256) }

    /// `text` with what the map can't draw left out (and the spaces that leaves doubled closed up).
    static func drawable(_ text: String) -> String {
        guard !text.unicodeScalars.allSatisfy(canDraw) else { return text }
        var kept = String.UnicodeScalarView()
        kept.append(contentsOf: text.unicodeScalars.filter(canDraw))
        return String(kept).split(separator: " ", omittingEmptySubsequences: true).joined(separator: " ")
    }
}
