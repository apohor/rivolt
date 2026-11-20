import CoreLocation
import Foundation
import MapLibre
import UIKit

/// The basemap under the share card's route (the web's shareBasemap.ts): the area the drive went through, drawn by MapLibre's
/// snapshotter from our own tile archive and laid under the line by the card painter. The style is the app's dark basemap with no
/// symbol layer (so no labels at any zoom), no glyphs and no sprite; the only request is the server's tile archive. The route is
/// the server's trimmed postcard route (both ends and the saved home already cut off), so the map shows no more than the line did.
/// The map is framed to the segments with at least 10% padding and never past zoom 12, with no marker, pin, scale or compass, and
/// the result is pixels only: coordinates leave this file only as the line's pixel positions in the picture. Never MKMapSnapshotter
/// (Apple's maps would be a second place that sees the area).
enum ShareBasemap {
    /// The card's map band, in card pixels: always this wide, and as tall as the card's layout leaves (at most `maxHeight`).
    static let width = 920.0
    static let maxHeight = 300.0
    static let maxZoom = 12.0
    static let pad = 0.1
    /// Whether drawing the map failed, so the sheet may say "Map unavailable": a card with no band (too full to give the map room) never asked.
    static func failed(band: Double?, picture: Picture?) -> Bool { band != nil && picture == nil }
    /// MapLibre's world is 512 points wide at zoom 0.
    private static let tile = 512.0
    private static let timeoutSeconds = 15.0

    /// The map picture, with the segments projected into its point space (each kept apart, so a cut-off stretch is never joined).
    struct Picture {
        let image: UIImage
        let lines: [[CGPoint]]
        let size: CGSize
    }

    /// The card's map style: the app's dark basemap whatever the appearance (the card is a fixed dark image), labels off: every symbol
    /// layer dropped and the glyph and sprite URLs with them. Nil when the bundled style is missing.
    static func style(server: URL, bundle: Bundle = .main) -> String? {
        guard let raw = MapStyle.json(dark: true, server: server, bundle: bundle),
              var root = (try? JSONSerialization.jsonObject(with: Data(raw.utf8))) as? [String: Any],
              let layers = root["layers"] as? [[String: Any]] else { return nil }
        root["layers"] = layers.filter { ($0["type"] as? String) != "symbol" }
        root["glyphs"] = nil
        root["sprite"] = nil
        guard let data = try? JSONSerialization.data(withJSONObject: root, options: [.withoutEscapingSlashes]) else { return nil }
        return String(decoding: data, as: UTF8.self)
    }

    /// The camera that fits every path into `size` with `pad` on each side, never past `maxZoom` (Web Mercator, as the map is).
    static func frame(_ paths: [[DriveRoute.Point]], size: CGSize) -> (center: CLLocationCoordinate2D, zoom: Double)? {
        let all = paths.flatMap { $0 }
        guard !all.isEmpty else { return nil }
        func mx(_ lon: Double) -> Double { lon / 360 + 0.5 }
        func my(_ lat: Double) -> Double { 0.5 - log(tan(.pi / 4 + lat * .pi / 360)) / (2 * .pi) }
        let xs = all.map { mx($0.lon) }, ys = all.map { my($0.lat) }
        let (x0, x1, y0, y1) = (xs.min()!, xs.max()!, ys.min()!, ys.max()!)
        let fit = 1 - 2 * pad
        let z = min(log2(size.width * fit / (tile * max(x1 - x0, 1e-12))), log2(size.height * fit / (tile * max(y1 - y0, 1e-12))))
        let cy = (y0 + y1) / 2
        let lat = atan(sinh(.pi * (1 - 2 * cy))) * 180 / .pi
        return (CLLocationCoordinate2D(latitude: lat, longitude: ((x0 + x1) / 2 - 0.5) * 360), min(maxZoom, max(0, z)))
    }

    /// The picture for the postcard `segments`, or nil when it can't be drawn (no bundled style, a tile read that failed, no answer in
    /// time): the card then carries the plain line.
    @MainActor
    static func render(segments: [String], height: Double, server: URL, token: String?) async -> Picture? {
        let size = CGSize(width: width, height: height)
        let paths = segments.map { DriveRoute.decode($0, precision: 6) }.filter { $0.count >= 2 }
        guard let frame = frame(paths, size: size), let style = style(server: server) else { return nil }
        MapNetwork.prepare(server: server, token: token)
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("share-basemap-\(UUID().uuidString).json")
        guard (try? Data(style.utf8).write(to: file, options: [.atomic, .completeFileProtection])) != nil else { return nil }
        defer { try? FileManager.default.removeItem(at: file) }

        let options = MLNMapSnapshotOptions(styleURL: file, camera: MLNMapCamera(lookingAtCenter: frame.center, altitude: 1, pitch: 0, heading: 0), size: size)
        options.zoomLevel = frame.zoom
        options.scale = 2
        options.showsLogo = false
        options.showsAttribution = false
        let snapshotter = MLNMapSnapshotter(options: options)
        let snapshot: MLNMapSnapshot? = await withCheckedContinuation { cont in
            let once = Once(cont)
            snapshotter.start(with: .main) { snap, _ in once.resume(snap) }
            DispatchQueue.main.asyncAfter(deadline: .now() + timeoutSeconds) {
                snapshotter.cancel()
                once.resume(nil)
            }
        }
        guard let snapshot else { return nil }
        let lines = paths.map { $0.map { snapshot.point(for: CLLocationCoordinate2D(latitude: $0.lat, longitude: $0.lon)) } }
        return Picture(image: snapshot.image, lines: lines, size: size)
    }

    /// Resumes a continuation once, whichever of the snapshot and the timeout comes first.
    private final class Once: @unchecked Sendable {
        private let lock = NSLock()
        private var cont: CheckedContinuation<MLNMapSnapshot?, Never>?
        init(_ cont: CheckedContinuation<MLNMapSnapshot?, Never>) { self.cont = cont }
        func resume(_ snap: MLNMapSnapshot?) {
            lock.lock()
            let c = cont
            cont = nil
            lock.unlock()
            c?.resume(returning: snap)
        }
    }
}
