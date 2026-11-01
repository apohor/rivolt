import Foundation

/// The web's road-snapped, speed-coloured drive route (web/src/lib/trace.ts, web/src/components/DriveMap.tsx and the
/// drive detail page's sample handling), kept out of the views so it can be tested. Everything here works on the one
/// drive on screen and lives only as long as that screen.
enum RouteSnap {
    struct Point: Equatable {
        let lat: Double
        let lon: Double
        /// Unix seconds, when known.
        var t: Double?
        /// mph, when known.
        var s: Double?
    }

    typealias Coord = DriveRoute.Point

    // MARK: Speed

    /// The web's five buckets (mph): <5, 5–25, 25–50, 50–65, 65+.
    enum Speed: Int, CaseIterable {
        case under5, to25, to50, to65, over65

        static func bucket(_ mph: Double?) -> Speed? {
            guard let mph, mph.isFinite else { return nil }
            switch mph {
            case ..<5: return .under5
            case ..<25: return .to25
            case ..<50: return .to50
            case ..<65: return .to65
            default: return .over65
            }
        }

        var label: String { ["<5", "5–25", "25–50", "50–65", "65+"][rawValue] }
        /// The band named by its lower bound, for the small legend: "<5", "5", "25", "50", "65+".
        var shortLabel: String { ["<5", "5", "25", "50", "65+"][rawValue] }
    }

    // MARK: Trace cleaning (trace.ts)

    static let maxSaneSpeedMps = 45.0
    static let gapTimeS = 120.0
    static let gapDistM = 500.0

    static func haversine(_ a: Point, _ b: Point) -> Double {
        let r = 6_371_000.0
        let p1 = a.lat * .pi / 180, p2 = b.lat * .pi / 180
        let dp = (b.lat - a.lat) * .pi / 180, dl = (b.lon - a.lon) * .pi / 180
        let x = pow(sin(dp / 2), 2) + cos(p1) * cos(p2) * pow(sin(dl / 2), 2)
        return 2 * r * asin(min(1, x.squareRoot()))
    }

    /// Drops unusable points, consecutive duplicates and single-sample jumps faster than ~100 mph each way, and
    /// collapses runs of stopped samples to their two ends.
    static func clean(_ pts: [Point]) -> [Point] {
        var s = pts.filter { $0.lat.isFinite && $0.lon.isFinite && abs($0.lat) <= 90 && abs($0.lon) <= 180 }
        guard !s.isEmpty else { return s }
        s = s.enumerated().filter { i, p in i == 0 || p.lat != s[i - 1].lat || p.lon != s[i - 1].lon }.map(\.element)
        if s.count >= 3 {
            var kept = [s[0]]
            for i in 1..<(s.count - 1) {
                let prev = kept[kept.count - 1], cur = s[i], next = s[i + 1]
                let dtIn = cur.t.flatMap { c in prev.t.map { max(0.5, c - $0) } } ?? 1
                let dtOut = next.t.flatMap { n in cur.t.map { max(0.5, n - $0) } } ?? 1
                if haversine(prev, cur) / dtIn > maxSaneSpeedMps && haversine(cur, next) / dtOut > maxSaneSpeedMps { continue }
                kept.append(cur)
            }
            kept.append(s[s.count - 1])
            s = kept
        }
        if s.count >= 3 {
            var kept: [Point] = []
            var inRun = false
            for i in 0..<s.count {
                let cur = s[i]
                if i > 0 {
                    let prev = s[i - 1]
                    let stopped = haversine(prev, cur) < 1 && (cur.t == nil || prev.t == nil || cur.t! - prev.t! < 60)
                    if stopped { inRun = true; continue }
                }
                if inRun {
                    let anchor = s[i - 1]
                    if kept.last != anchor { kept.append(anchor) }
                    inRun = false
                }
                kept.append(cur)
            }
            if inRun { kept.append(s[s.count - 1]) }
            s = kept
        }
        return s
    }

    struct Gap: Equatable {
        let from: Coord
        let to: Coord
    }

    /// Cuts the trace wherever samples are ≥ 2 minutes or ≥ 500 m apart; single-point pieces are dropped, their gaps
    /// kept.
    static func split(_ pts: [Point]) -> (segments: [[Point]], gaps: [Gap]) {
        guard let first = pts.first else { return ([], []) }
        var segs: [[Point]] = [[first]]
        var gaps: [Gap] = []
        for i in 1..<pts.count {
            let prev = pts[i - 1], cur = pts[i]
            let dt = (cur.t != nil && prev.t != nil) ? cur.t! - prev.t! : 0
            if dt >= gapTimeS || haversine(prev, cur) >= gapDistM {
                gaps.append(Gap(from: Coord(lat: prev.lat, lon: prev.lon), to: Coord(lat: cur.lat, lon: cur.lon)))
                segs.append([cur])
            } else {
                segs[segs.count - 1].append(cur)
            }
        }
        return (segs.filter { $0.count >= 2 }, gaps)
    }

    enum Quality: Equatable { case trivial, short, normal }

    /// Short, sparse stretches are drawn as recorded: a matcher would invent a road for them.
    static func classify(_ seg: [Point]) -> Quality {
        guard seg.count >= 5 else { return .trivial }
        var d = 0.0
        for i in 1..<seg.count { d += haversine(seg[i - 1], seg[i]) }
        if d < 200 { return .trivial }
        return d < 800 ? .short : .normal
    }

    // MARK: The drive's points (the detail page)

    /// What the map draws from: the in-drive path with speeds, and the pins at either end.
    struct Path: Equatable {
        /// The parked start, the in-drive samples, and the parked end (unless it's the same spot), with speeds (0 parked).
        let points: [Point]
        let start: Coord?
        let end: Coord?
        let sameSpot: Bool
    }

    /// The web's rules: the start pin is the last parked fix in the 10 minutes before the drive (else the drive's own
    /// start), the end pin the first parked fix in the 10 minutes after (else its end). The path is the samples from
    /// the start to the end time, leading and trailing parked frames trimmed. Synthesised times 60 s outside the drive
    /// keep the pins' points monotonic for the matcher.
    static func path(drive: DriveRoute, samples: [DriveSample]) -> Path? {
        guard let started = drive.startedAt, let ended = drive.endedAt else { return nil }
        func real(_ lat: Double, _ lon: Double) -> Bool { lat.isFinite && lon.isFinite && !(lat == 0 && lon == 0) }
        let sorted = samples.sorted { $0.at < $1.at }
        let homeStart = sorted.last { $0.at >= started.addingTimeInterval(-600) && $0.at < started && $0.shiftState == "P" && real($0.lat, $0.lon) }
        let homeEnd = sorted.first { $0.at > ended && $0.at <= ended.addingTimeInterval(600) && $0.shiftState == "P" && real($0.lat, $0.lon) }
        var inDrive = sorted.filter { $0.at >= started && $0.at <= ended }
        while inDrive.first?.shiftState == "P" { inDrive.removeFirst() }
        while inDrive.last?.shiftState == "P" { inDrive.removeLast() }
        // In GPS-fix order, not arrival order: older and repeated fixes would jump the line back along the road (DriveTrack).
        inDrive = DriveTrack.clean(inDrive)
        let valid = inDrive.filter { real($0.lat, $0.lon) }.map {
            Point(lat: $0.lat, lon: $0.lon, t: $0.at.timeIntervalSince1970, s: $0.speedMph)
        }
        let start = homeStart.map { Coord(lat: $0.lat, lon: $0.lon) } ?? (real(drive.startLat, drive.startLon) ? Coord(lat: drive.startLat, lon: drive.startLon) : nil)
        let end = homeEnd.map { Coord(lat: $0.lat, lon: $0.lon) } ?? (real(drive.endLat, drive.endLon) ? Coord(lat: drive.endLat, lon: drive.endLon) : nil)
        let lineStart = start ?? valid.first.map { Coord(lat: $0.lat, lon: $0.lon) }
        let lineEnd = end ?? valid.last.map { Coord(lat: $0.lat, lon: $0.lon) }
        var same = false
        if let a = lineStart, let b = lineEnd { same = abs(a.lat - b.lat) < 0.0005 && abs(a.lon - b.lon) < 0.0005 }
        let firstT = valid.first { $0.t != nil }?.t
        let lastT = valid.last { $0.t != nil }?.t
        var pts: [Point] = []
        if let start { pts.append(Point(lat: start.lat, lon: start.lon, t: firstT.map { $0 - 60 }, s: 0)) }
        pts += valid
        if let end, !same { pts.append(Point(lat: end.lat, lon: end.lon, t: lastT.map { $0 + 60 }, s: 0)) }
        guard !valid.isEmpty else { return nil }
        return Path(points: pts, start: lineStart, end: same ? nil : lineEnd, sameSpot: same)
    }

    // MARK: Snapping (DriveMap's snapToRoads)

    /// One stretch of line with a speed bucket (nil: no speed known).
    struct Piece: Equatable {
        let coords: [Coord]
        let speed: Speed?
    }

    /// One unbroken stretch of line with the speed (mph, nil when unknown) at each vertex: what the gradient colours.
    struct Run: Equatable {
        let coords: [Coord]
        let speeds: [Double?]
        /// The road router guessed this stretch (there was no GPS along it): it is drawn so it plainly looks guessed (`GradientStyle`).
        var filled = false

        /// Metres along the line to each vertex, as a share of the whole (0 at the first, 1 at the last): `line-progress`.
        var progress: [Double] {
            var d = [0.0]
            for i in 1..<max(1, coords.count) { d.append(d[i - 1] + RouteSnap.haversine(Point(lat: coords[i - 1].lat, lon: coords[i - 1].lon), Point(lat: coords[i].lat, lon: coords[i].lon))) }
            guard let total = d.last, total > 0 else { return d }
            return d.map { $0 / total }
        }

        /// The speeds with the gaps filled from the previous known one (else the next), so the colour carries on instead of dropping to the bottom.
        var filledSpeeds: [Double] {
            let known = speeds.map { $0.flatMap { $0.isFinite ? $0 : nil } }
            guard let first = known.compactMap({ $0 }).first else { return [] }
            var last = first
            return known.map { v in if let v { last = v }; return last }
        }
    }

    /// A run across an unmatched stretch the road router filled: its speed goes from the speed beside its first point to the speed beside its
    /// last, in proportion to the distance along it, so the colour carries across the gap instead of the line going neutral. Nil without a speed
    /// at either end (a drive with no speeds at all has no gradient to carry).
    static func filledRun(_ coords: [Coord], from: Double?, to: Double?) -> Run? {
        guard coords.count >= 2, let a = from ?? to, let b = to ?? from, a.isFinite, b.isFinite else { return nil }
        var d = [0.0]
        for i in 1..<coords.count { d.append(d[i - 1] + haversine(Point(lat: coords[i - 1].lat, lon: coords[i - 1].lon), Point(lat: coords[i].lat, lon: coords[i].lon))) }
        let total = d.last ?? 0
        return Run(coords: coords, speeds: d.map { total > 0 ? a + (b - a) * ($0 / total) : a }, filled: true)
    }

    /// How a gradient run is drawn: a recorded one is the full line with a faint glow; one the router filled is the same colours, but about 60% as
    /// wide at half the opacity and with no glow, so it never passes for measured speed. (MapLibre's line-gradient cannot be dashed.)
    struct GradientStyle: Equatable {
        let width: Double
        let opacity: Double
        let glow: (width: Double, opacity: Double)?

        static func of(_ run: Run) -> GradientStyle {
            run.filled ? GradientStyle(width: 2.1, opacity: 0.5, glow: nil) : GradientStyle(width: 3.5, opacity: 0.95, glow: (9, 0.18))
        }

        static func == (a: GradientStyle, b: GradientStyle) -> Bool {
            a.width == b.width && a.opacity == b.opacity && a.glow?.width == b.glow?.width && a.glow?.opacity == b.glow?.opacity
        }
    }

    /// The most separate runs the gradient is drawn for (each is its own map layer); a drive cut into more than this keeps the speed buckets.
    static let maxGradientRuns = 24

    struct Drawing: Equatable {
        var pieces: [Piece]
        /// Road-routed fills across an unmatched stretch: solid, neutral.
        var routedGaps: [[Coord]]
        /// Stretches nobody could fill: dashed, neutral.
        var gaps: [Gap]
        var start: Coord?
        var end: Coord?
        /// Whether any point had a speed (then the legend shows).
        var hasSpeed: Bool
        /// Charging stops of a planned trip, numbered from 0 in the order of the plan's list.
        var stops: [Stop] = []
        /// The pin (index in `stops`) the person picked in the list, drawn larger in the accent colour.
        var focus: Int?
        /// Fast chargers along the route the person asked to see, drawn small under the stops. A focus past the stops points here.
        var along: [Stop] = []
        /// The line again as unbroken runs with a speed at each vertex, for the gradient (empty where there are no speeds, or on a plan).
        var runs: [Run] = []

        /// The 5th to 95th percentile of the drive's speeds (the web's scale), nil when the drive barely varies: then the buckets stay.
        var speedRange: (lo: Double, hi: Double)? { RouteGradient.range(values: runs.flatMap(\.filledSpeeds), metric: .speed) }

        /// Whether the line is drawn as a smooth gradient of speed (it needs speeds that vary, and a number of runs one layer each can carry).
        var usesGradient: Bool { !runs.isEmpty && runs.count <= RouteSnap.maxGradientRuns && speedRange != nil }

        struct Stop: Equatable {
            var coord: Coord
            var label: String
        }

        var allCoords: [Coord] { pieces.flatMap(\.coords) + routedGaps.flatMap { $0 } }

        /// The speed bands this drive uses, slowest first (what the legend lists).
        var usedSpeeds: [Speed] { Speed.allCases.filter { band in pieces.contains { $0.speed == band } } }
    }

    /// The line coloured by speed: a new piece whenever the bucket changes, each sharing its first point with the
    /// previous piece's last so the line has no breaks.
    static func pieces(_ coords: [Coord], speeds: [Double?]) -> [Piece] {
        guard coords.count >= 2 else { return [] }
        let buckets = speeds.count == coords.count ? speeds.map(Speed.bucket) : Array(repeating: nil, count: coords.count)
        var out: [Piece] = []
        var start = 0
        var current = buckets[0]
        for i in 1..<coords.count where buckets[i] != current {
            out.append(Piece(coords: Array(coords[start...i]), speed: current))
            start = i
            current = buckets[i]
        }
        out.append(Piece(coords: Array(coords[start...]), speed: current))
        return out.filter { $0.coords.count >= 2 }
    }

    /// The coordinates with their speeds as one run (none when there are fewer than two points, or no speeds at all).
    static func runs(_ coords: [Coord], speeds: [Double?]) -> [Run] {
        guard coords.count >= 2, speeds.count == coords.count, speeds.contains(where: { $0?.isFinite == true }) else { return [] }
        return [Run(coords: coords, speeds: speeds)]
    }

    /// The recorded trace as drawn before (or without) snapping.
    static func raw(_ path: Path) -> Drawing {
        let coords = path.points.map { Coord(lat: $0.lat, lon: $0.lon) }
        return Drawing(pieces: pieces(coords, speeds: path.points.map(\.s)), routedGaps: [], gaps: [], start: path.start, end: path.end,
                       hasSpeed: path.points.contains { $0.s?.isFinite == true }, runs: runs(coords, speeds: path.points.map(\.s)))
    }

    /// The speed at a snapped point: the nearest trace point's.
    static func nearestSpeed(_ c: Coord, in trace: [Point]) -> Double? {
        var best = 0
        var bestD = Double.infinity
        for (i, p) in trace.enumerated() {
            let dy = (p.lat - c.lat) * 111_111
            let dx = (p.lon - c.lon) * 111_111 * cos(c.lat * .pi / 180)
            let d = dx * dx + dy * dy
            if d < bestD { bestD = d; best = i }
        }
        return trace.isEmpty ? nil : trace[best].s
    }

    static let maxTrace = 1500
    static let tailToleranceM = 200.0
}

/// The two Valhalla calls, kept apart from `APIClient` so tests can fake them.
protocol RouteSnapSource: Sendable {
    func valhallaTrace(_ body: ValhallaTraceRequest) async throws -> ValhallaTraceResponse
    func valhallaRoute(_ body: ValhallaRouteRequest) async throws -> ValhallaRouteResponse
}

extension APIClient: RouteSnapSource {}

extension RouteSnap {
    /// Snaps `path` to roads as the web does: each clean segment through trace_route (walk_or_snap), short ones drawn
    /// as recorded, the ends wrapped with gaps when the match stops short, and every gap filled through route where
    /// it can be. Nil when nothing could be snapped (the caller keeps the recorded line).
    static func snap(_ path: Path, source: some RouteSnapSource) async -> Drawing? {
        let recorded = path.points
        let cleaned = clean(recorded)
        guard cleaned.count >= 2 else { return nil }
        let (segments, splitGaps) = split(cleaned)
        guard !segments.isEmpty else { return nil }
        var pieces: [Piece] = []
        var allRuns: [Run] = []
        var gaps = splitGaps
        var snappedAny = false
        for seg in segments {
            if Task.isCancelled { return nil }
            let rawCoords = seg.map { Coord(lat: $0.lat, lon: $0.lon) }
            if classify(seg) == .trivial {
                pieces += RouteSnap.pieces(rawCoords, speeds: seg.map(\.s))
                allRuns += runs(rawCoords, speeds: seg.map(\.s))
                continue
            }
            guard let matched = await trace(seg, source: source), !matched.chunks.isEmpty else {
                pieces += RouteSnap.pieces(rawCoords, speeds: seg.map(\.s))
                allRuns += runs(rawCoords, speeds: seg.map(\.s))
                continue
            }
            let chunks = matched.chunks, inner = matched.gaps
            snappedAny = true
            for c in chunks {
                let speeds = c.map { nearestSpeed($0, in: recorded) }
                pieces += RouteSnap.pieces(c, speeds: speeds)
                allRuns += runs(c, speeds: speeds)
            }
            gaps += inner
            let head = Point(lat: seg[0].lat, lon: seg[0].lon), firstSnap = chunks[0][0]
            if haversine(head, Point(lat: firstSnap.lat, lon: firstSnap.lon)) > tailToleranceM {
                gaps.append(Gap(from: Coord(lat: head.lat, lon: head.lon), to: firstSnap))
            }
            let tail = seg[seg.count - 1], lastSnap = chunks[chunks.count - 1][chunks[chunks.count - 1].count - 1]
            if haversine(Point(lat: lastSnap.lat, lon: lastSnap.lon), tail) > tailToleranceM {
                gaps.append(Gap(from: lastSnap, to: Coord(lat: tail.lat, lon: tail.lon)))
            }
        }
        guard snappedAny else { return nil }
        // Fill the gaps along roads where Valhalla can; the rest stay dashed.
        var routed: [[Coord]] = []
        var remaining: [Gap] = []
        await withTaskGroup(of: (Int, [Coord]?).self) { group in
            for (i, g) in gaps.enumerated() {
                group.addTask { (i, await route(g, source: source)) }
            }
            var filled = [[Coord]?](repeating: nil, count: gaps.count)
            for await (i, r) in group { filled[i] = r }
            for (i, g) in gaps.enumerated() {
                if let r = filled[i], r.count > 1 {
                    routed.append(r)
                    if let run = filledRun(r, from: nearestSpeed(r[0], in: recorded), to: nearestSpeed(r[r.count - 1], in: recorded)) { allRuns.append(run) }
                } else { remaining.append(g) }
            }
        }
        if Task.isCancelled { return nil }
        return Drawing(pieces: pieces, routedGaps: routed, gaps: remaining, start: path.start, end: path.end,
                       hasSpeed: recorded.contains { $0.s?.isFinite == true }, runs: allRuns)
    }

    /// One trace_route call: the matched sub-routes, and the unmatched stretches between them (in input points).
    static func trace(_ pts: [Point], source: some RouteSnapSource) async -> (chunks: [[Coord]], gaps: [Gap])? {
        var traced = pts
        if traced.count > maxTrace {
            let step = Int((Double(traced.count) / Double(maxTrace)).rounded(.up))
            traced = traced.enumerated().filter { $0.offset % step == 0 }.map(\.element)
            if traced.last != pts.last, let last = pts.last { traced.append(last) }
        }
        let body = ValhallaTraceRequest(shape: traced.map { .init(lat: $0.lat, lon: $0.lon, time: $0.t) })
        guard let r = try? await source.valhallaTrace(body), let matchings = r.matchings, !matchings.isEmpty else { return nil }
        let chunks = matchings.map { m in m.geometry.map { DriveRoute.decode($0, precision: 6) } ?? [] }
        let tps = r.tracepoints ?? []
        var first = [Int](repeating: -1, count: matchings.count), last = [Int](repeating: -1, count: matchings.count)
        for (i, tp) in tps.enumerated() {
            guard let k = tp?.matchingsIndex, k >= 0, k < matchings.count else { continue }
            if first[k] == -1 { first[k] = i }
            last[k] = i
        }
        var gaps: [Gap] = []
        if matchings.count > 1 {
            for k in 1..<matchings.count where last[k - 1] >= 0 && first[k] >= 0 && last[k - 1] < traced.count && first[k] < traced.count {
                let a = traced[last[k - 1]], b = traced[first[k]]
                gaps.append(Gap(from: Coord(lat: a.lat, lon: a.lon), to: Coord(lat: b.lat, lon: b.lon)))
            }
        }
        let valid = chunks.filter { $0.count > 1 }
        return valid.isEmpty ? nil : (valid, gaps)
    }

    /// A road-following line between a gap's ends, or nil.
    static func route(_ g: Gap, source: some RouteSnapSource) async -> [Coord]? {
        let body = ValhallaRouteRequest(locations: [.init(lat: g.from.lat, lon: g.from.lon), .init(lat: g.to.lat, lon: g.to.lon)])
        guard let r = try? await source.valhallaRoute(body), let legs = r.trip?.legs, !legs.isEmpty else { return nil }
        var out: [Coord] = []
        for (i, leg) in legs.enumerated() {
            var seg = leg.shape.map { DriveRoute.decode($0, precision: 6) } ?? []
            if i > 0, !seg.isEmpty { seg.removeFirst() }
            out += seg
        }
        return out.count > 1 ? out : nil
    }
}
