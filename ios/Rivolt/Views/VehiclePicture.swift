import Foundation

/// Which picture the Now hero shows: the same rule as the web's lib/vehicleImage.ts, so both look alike.
enum VehiclePicture {
    /// Older placement strings (side-exterior-3qfront-driver, ...) fold into the same names; interior ones are never shown.
    static func canon(_ placement: String?) -> String {
        let p = (placement ?? "").lowercased()
        if p.isEmpty || p.contains("interior") { return "" }
        if p.contains("charging") { return "side-charging" }
        if p.contains("3qfront") || p == "three-quarter" { return "three-quarter" }
        if p.contains("3qrear") || p == "side-rear" { return "side-rear" }
        if p == "side" || p.hasPrefix("side-exterior") { return "side" }
        if p.hasPrefix("front") { return "front" }
        if p.hasPrefix("rear") { return "rear" }
        return p
    }

    /// The first placement in `wants` that exists wins (then three-quarter, side, side-rear, front, rear); within it the design
    /// that matches the page and the large size.
    static func pick(_ images: [VehicleImage]?, wants: [String], light: Bool) -> URL? {
        guard let images, !images.isEmpty else { return nil }
        let design = light ? "light" : "dark"
        func rank(_ i: VehicleImage) -> Int { (i.design == design ? 0 : 2) + (i.size == "large" ? 0 : 1) }
        for want in wants + ["three-quarter", "side", "side-rear", "front", "rear"] {
            let hits = images.filter { canon($0.placement) == want }
            if hits.isEmpty { continue }
            // A stable choice: the lowest rank, the first listed among equals.
            var best = hits[0]
            for h in hits.dropFirst() where rank(h) < rank(best) { best = h }
            if let url = URL(string: best.url), url.scheme == "https" || Self.allowsFile(url) { return url }
        }
        return nil
    }

    /// A local file stands in for Rivian's picture in Debug builds only, so the UI tests and the design shots can show one without a network.
    private static func allowsFile(_ url: URL) -> Bool {
        #if DEBUG
        return url.isFileURL
        #else
        return false
        #endif
    }

    /// Now: driving shows the side view, every other state the three-quarter view. Rivian's side-charging art is a cropped close-up of the
    /// front half, which read as a picture cut off at the right (#2249), so it is never chosen here; the plugged-in state is the plug badge
    /// and glow (`PlugPicture`) on the whole truck. `powerKw` and `plugPicture` stay in the signature for the callers.
    static func forNow(_ images: [VehicleImage]?, kind: NowSnapshot.State, powerKw: Double?, light: Bool, plugPicture: Bool = false) -> URL? {
        pick(images, wants: kind == .driving ? ["side"] : [], light: light)
    }

    /// What the Live picture reads from the truck's state: the gear and the four closures. The web's `pickImageForState` takes the same five.
    struct LiveInputs: Equatable {
        var gear: String?
        var doorsClosed: Bool?
        var frunkClosed: Bool?
        var liftgateClosed: Bool?
        var tonneauClosed: Bool?

        init(gear: String? = nil, doorsClosed: Bool? = nil, frunkClosed: Bool? = nil, liftgateClosed: Bool? = nil, tonneauClosed: Bool? = nil) {
            self.gear = gear; self.doorsClosed = doorsClosed; self.frunkClosed = frunkClosed; self.liftgateClosed = liftgateClosed; self.tonneauClosed = tonneauClosed
        }

        init(_ state: VehicleState) {
            self.init(gear: state.gear, doorsClosed: state.doorsClosed, frunkClosed: state.frunkClosed, liftgateClosed: state.liftgateClosed, tonneauClosed: state.tonneauClosed)
        }
    }

    /// Live: the angle that illustrates the truck's state right now, the same rule as the web's `pickImageForState`: driving shows the side view,
    /// an open frunk the front, an open liftgate or tonneau the rear, an open door the side; the first of those that exists wins. A closure the truck
    /// has not reported counts as shut, on both platforms.
    /// Side-charging art is never chosen, as on Now.
    static func live(_ images: [VehicleImage]?, state: LiveInputs?, light: Bool) -> URL? {
        var wants: [String] = []
        if let state {
            if ["D", "R", "N"].contains((state.gear ?? "").uppercased()) { wants += ["side", "three-quarter"] }
            if state.frunkClosed == false { wants += ["front", "three-quarter"] }
            if state.liftgateClosed == false || state.tonneauClosed == false { wants += ["rear", "side-rear"] }
            if state.doorsClosed == false { wants += ["side"] }
        }
        return pick(images, wants: wants, light: light)
    }

    /// The Live pictures to try in order: the state's pick, then the vehicle's own `image_url`; one that failed to load is skipped
    /// (the web's `pickLiveImage`).
    static func liveCandidates(_ images: [VehicleImage]?, imageURL: String?, state: LiveInputs?, light: Bool, failed: Set<URL> = []) -> [URL] {
        var out: [URL] = []
        if let u = live(images, state: state, light: light) { out.append(u) }
        if let raw = imageURL, let u = URL(string: raw), u.scheme == "https" || allowsFile(u) { out.append(u) }
        return out.filter { !failed.contains($0) }
    }
}
