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
            if let url = URL(string: best.url), url.scheme == "https" { return url }
        }
        return nil
    }

    /// Now: charging shows the plugged side view, driving the side view, otherwise the three-quarter view.
    static func forNow(_ images: [VehicleImage]?, kind: NowSnapshot.State, light: Bool) -> URL? {
        pick(images, wants: kind == .charging ? ["side-charging"] : kind == .driving ? ["side"] : [], light: light)
    }
}
