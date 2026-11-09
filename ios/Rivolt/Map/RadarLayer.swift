import Foundation
import MapLibre

/// The radar on a map: one frame of the server's radar tiles, drawn under the route line, the pins and the pills (never over them). NOAA's own colours,
/// not recoloured; about 60 % opaque so the roads stay readable.
struct RadarLayer: Equatable {
    var server: URL
    var frameUnix: Int
    var minZoom: Int = 3
    var maxZoom: Int = 9

    static let sourceID = "rivolt-radar"
    static let opacity = 0.6

    /// The tile URL template for this frame: a new frame is a new URL, so MapLibre never shows a cached older one.
    var tileTemplate: String { "\(MapStyle.origin(server))/api/maps/radar/{z}/{x}/{y}.png?t=\(frameUnix)" }

    /// Puts the radar on `style` (replacing any earlier frame), below the first of the route's own layers, or removes it when `layer` is nil.
    static func apply(_ layer: RadarLayer?, to style: MLNStyle) {
        if let old = style.layer(withIdentifier: sourceID) { style.removeLayer(old) }
        if let old = style.source(withIdentifier: sourceID) { style.removeSource(old) }
        guard let layer else { return }
        let source = MLNRasterTileSource(identifier: sourceID, tileURLTemplates: [layer.tileTemplate],
                                         options: [.tileSize: 256, .minimumZoomLevel: layer.minZoom, .maximumZoomLevel: layer.maxZoom])
        style.addSource(source)
        let raster = MLNRasterStyleLayer(identifier: sourceID, source: source)
        raster.rasterOpacity = NSExpression(forConstantValue: opacity)
        if let route = style.layers.first(where: { $0.identifier.hasPrefix(RouteMapView.sourceID) }) {
            style.insertLayer(raster, below: route)
        } else {
            style.addLayer(raster)
        }
    }
}
