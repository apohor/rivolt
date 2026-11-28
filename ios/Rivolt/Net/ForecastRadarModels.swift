import Foundation

/// `GET /api/maps/forecast-radar/hours` (Pro): the newest usable NOAA RRFS cycle, the grid's box, and the hours it can draw. The tiles are the server's own,
/// `/api/maps/forecast-radar/{cycle}/{fh}/{z}/{x}/{y}.png`, so the phone never calls NOAA.
struct ForecastHours: Decodable, Equatable {
    struct Hour: Decodable, Equatable {
        var fh: Int
        var validUnix: Int
    }
    var cycle: String
    var credit: String?
    /// West, south, east, north.
    var bounds: [Double]
    var hours: [Hour]
    var horizonUnix: Int?
}

protocol ForecastSource: Sendable {
    func forecastHours() async throws -> ForecastHours
}

extension APIClient: ForecastSource {}
