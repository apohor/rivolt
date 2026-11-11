import Foundation
import Observation

/// The planner's Radar button: whether the server has radar at all, whether the person turned it on, and the newest frame. The radar is NOAA/NWS
/// base reflectivity through the server's own proxy; it is live (the past couple of hours), not a forecast. A server without radar (404) or with its
/// feed down (503) simply has no button.
@MainActor
@Observable
final class RadarModel {
    enum Availability: Equatable { case unknown, available, unavailable }

    private(set) var availability = Availability.unknown
    private(set) var isOn = false
    private(set) var frame: RadarFrame?
    private var minZoom = 3
    private var maxZoom = 9

    /// How often the newest frame is asked for while the radar is on (the frames come about every ten minutes).
    var refreshEvery: Duration = .seconds(180)

    /// Asks the server's config once (per session and server) whether it has radar mounted: only then is there a button. Anything but an answer that
    /// says so means no button and no error.
    func probe(source: some RadarSource) async {
        guard availability == .unknown else { return }
        do {
            let config = try await source.mapsConfig()
            guard config.hasRadar else { availability = .unavailable; return }
            minZoom = config.tiles?.radarMinzoom ?? 3
            maxZoom = config.tiles?.radarMaxzoom ?? 9
            availability = .available
        } catch {
            availability = .unavailable
        }
    }

    /// The button: off to on asks for the newest frame (a feed that is down means no layer and no button, quietly); on to off just turns it off.
    func toggle(source: some RadarSource) async {
        guard availability == .available else { return }
        if isOn { isOn = false; return }
        do {
            frame = try await source.radarFrame()
            isOn = true
        } catch APIError.server(let status, _, _) where status == 404 || status == 503 {
            availability = .unavailable
        } catch {
            // A blip: nothing turned on; the button is still there to tap again.
        }
    }

    private var defaultApplied = false

    /// The Radar default from the Trip planner settings: on for a person who set it, applied once per session when the server has radar (so a person who then
    /// turns it off on the map is not switched back on at the next plan). Off, or no radar here, changes nothing.
    func applyDefault(on: Bool, source: some RadarSource) async {
        guard !defaultApplied else { return }
        await probe(source: source)
        guard availability == .available else { return }
        defaultApplied = true
        if on, !isOn { await toggle(source: source) }
    }

    /// Another account or server: forget it all.
    func reset() {
        defaultApplied = false
        availability = .unknown
        isOn = false
        frame = nil
    }

    /// While the radar is on, a newer frame is fetched every few minutes; a feed that goes down takes the layer away quietly.
    func keepFresh(source: some RadarSource) async {
        while isOn, !Task.isCancelled {
            try? await Task.sleep(for: refreshEvery)
            if Task.isCancelled || !isOn { return }
            do {
                frame = try await source.radarFrame()
            } catch APIError.server(let status, _, _) where status == 404 || status == 503 {
                isOn = false
                availability = .unavailable
                return
            } catch {
                // A blip on the way: the frame on show stays.
            }
        }
    }

    /// The layer for the map: nil when the button is off (or there is no frame yet).
    func layer(server: URL) -> RadarLayer? {
        guard isOn, availability == .available, let frame else { return nil }
        return RadarLayer(server: server, frameUnix: frame.frameUnix, minZoom: minZoom, maxZoom: maxZoom)
    }
}

enum RadarLogic {
    static let kindLine = "Live radar, not a forecast."
    /// The words under the legend's bar; at the accessibility text sizes only the ends, so the three never crowd the bar.
    static func legendWords(accessibilitySize: Bool) -> [String] {
        accessibilitySize ? ["light", "heavy"] : ["light", "moderate", "heavy"]
    }

    /// "NOAA/NWS radar · 6:42 PM", in the zone of the phone (the live picture's time): the freshness and the credit in one line.
    static func asOf(unix: Int, zone: TimeZone = .current, locale: Locale = .current) -> String {
        let f = DateFormatter()
        f.locale = locale
        f.timeZone = zone
        f.dateStyle = .none
        f.timeStyle = .short
        return "NOAA/NWS radar · \(f.string(from: Date(timeIntervalSince1970: TimeInterval(unix))))"
    }
}
