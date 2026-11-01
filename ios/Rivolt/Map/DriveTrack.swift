import Foundation

/// The drive map's line is the drive's samples in the order they arrived. But a sample's position is a GPS fix with its own time
/// (`locationFixAt`), and the truck's modem hands back fixes late and again: a sample can repeat the previous fix, or carry one OLDER than
/// the one before it. Drawn in arrival order those older fixes jump the line back along the road and forward again, and the speed-coloured
/// line reads as a scribble. The web's `cleanTrack` (web/src/lib/driveTrack.ts) is the same rule.
///
/// `clean` keeps a sample only when its fix is newer than the newest fix already kept, so the kept positions run forward in fix time: older
/// fixes and exact repeats are dropped. A sample with no fix time is taken to be fixed when it arrived. A fix stamped later than its own
/// arrival is not believed (a fix cannot postdate the frame that carries it) and is treated as no fix time; otherwise one bad stamp would hide
/// every sample after it. Samples with no position pass through untouched.
enum DriveTrack {
    /// A fix may be stamped this long after its frame arrived (clock skew between the modem and the server) before it is not believed.
    static let fixAheadSlack: TimeInterval = 5

    static func clean(_ samples: [DriveSample]) -> [DriveSample] {
        // Arrival order, stable: the API sends them sorted, but this must not depend on it.
        let ordered = samples.enumerated().sorted { ($0.element.at, $0.offset) < ($1.element.at, $1.offset) }.map(\.element)
        var newest: Date?
        var out: [DriveSample] = []
        for s in ordered {
            guard hasPosition(s) else {
                out.append(s)
                continue
            }
            var fix = s.locationFixAt ?? s.at
            if fix > s.at.addingTimeInterval(fixAheadSlack) { fix = s.at }
            if let newest, fix <= newest { continue } // an older fix, or the same one again
            newest = fix
            out.append(s)
        }
        return out
    }

    private static func hasPosition(_ s: DriveSample) -> Bool {
        s.lat.isFinite && s.lon.isFinite && !(s.lat == 0 && s.lon == 0)
    }
}
