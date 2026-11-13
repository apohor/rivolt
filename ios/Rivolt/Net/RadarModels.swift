import Foundation

/// `GET /api/maps/radar/frame`: the newest radar frame and the recent ones (oldest first, about ten minutes apart). The tiles are the server's own
/// proxy of NOAA/NWS base reflectivity, `/api/maps/radar/{z}/{x}/{y}.png?t=<frameUnix>`, 256 px in EPSG:3857, transparent where there is no radar.
struct RadarFrame: Decodable, Equatable {
    /// RFC 3339 UTC.
    var frame: String
    var frameUnix: Int
    var framesUnix: [Int]?
    var minZoom: Int?
    var maxZoom: Int?
}

/// `GET /api/config` (public): the tile paths the server advertises; the radar keys are there only when radar is mounted on that server.
struct MapsConfig: Decodable, Equatable {
    struct Tiles: Decodable, Equatable {
        var radarUrl: String?
        var radarFrameUrl: String?
        var radarMinzoom: Int?
        var radarMaxzoom: Int?
    }
    var tiles: Tiles?
    var features: Features?

    /// Preview-only designs the server shows (`features.design_preview`, a list of keys). Absent, or anything but strings, means none.
    struct Features: Decodable, Equatable {
        var designPreview: [String]?

        init(designPreview: [String]? = nil) { self.designPreview = designPreview }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            designPreview = try? c.decode([String].self, forKey: .designPreview)
        }

        private enum CodingKeys: String, CodingKey { case designPreview }
    }

    var hasRadar: Bool { !(tiles?.radarUrl ?? "").isEmpty && !(tiles?.radarFrameUrl ?? "").isEmpty }
}

protocol RadarSource: Sendable {
    func mapsConfig() async throws -> MapsConfig
    func radarFrame() async throws -> RadarFrame
}

extension APIClient: RadarSource {}
