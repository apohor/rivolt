import CoreTransferable
import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// The month's share card, the web's `buildMonthShareModel` (lib/shareCard.ts and lib/statementCard.ts). The model is
/// the privacy guarantee: it carries display text only, built from the server's aggregate totals (no receipt, place,
/// id, coordinate or vehicle), so nothing else can reach the image. Cost and the off-peak share are opt-in.
struct MoneyShareCard: Equatable {
    struct Stat: Equatable {
        let label: String
        let value: String
    }

    static let footer = ["Made with Rivolt · rivolt.dev", "Unofficial. Not affiliated with Rivian."]

    let eyebrow: String
    let title: String
    let subtitle: String
    let stats: [Stat]
    let notes: [String]
    /// Six longer rows (the year card): painted a little smaller and closer so a value never meets its label.
    var compact = false

    /// "rivolt-month-2026-10.png": the kind and the month, never a truck or a place.
    static func fileName(_ m: LedgerMonth) -> String { "rivolt-month-\(m.period).png" }

    static func month(_ m: LedgerMonth, includeCost: Bool, offpeakSharePct: Double?, calendar: Calendar) -> MoneyShareCard {
        typealias L = MoneyLogic
        let n0 = { (x: Double) in Int(x.rounded()).formatted() }
        let miles = m.driving.miles
        var stats = [Stat(label: "Driven", value: "\(n0(miles)) mi"), Stat(label: "Energy added", value: "\(m.energy.includesEstimated == true ? "~" : "")\(n0(m.energy.kwhAdded)) kWh")]
        let used = m.driving.kwhUsedKnown ?? 0
        let withEnergy = m.driving.milesWithEnergy ?? 0
        if used > 0 && withEnergy > 0 { stats.append(Stat(label: "Efficiency", value: "\(L.fixed(withEnergy / used, 2)) mi/kWh")) }
        var notes: [String] = []
        var title = "\(n0(miles)) mi"
        var subtitle = "\(m.driving.drives) \(L.plural(m.driving.drives, "drive", "drives")) · \(m.energy.sessions) \(L.plural(m.energy.sessions, "charge", "charges"))"
        if includeCost {
            // The statement card's figures, behind the toggle.
            let cur = m.currency
            let e = m.energy
            title = L.money(L.statementTotal(m), cur)
            var parts = ["\(e.includesEstimated == true ? "~" : "")\(L.fixed(e.kwhAdded, 0)) kWh"]
            if miles > 0 { parts.append("\(L.fixed(miles, 0)) mi") }
            if let cpm = m.driving.costPerMile { parts.append("\(L.money(cpm, cur, digits: 3))/mi") }
            subtitle = parts.joined(separator: " · ")
            let buckets: [(String, Double)]
            if let pk = e.byPlaceKind {
                buckets = [("Home", pk.home.ac.cost), ("Away", pk.away.ac.cost), ("Fast charging", e.byKind.dcfc.cost)]
                    + (pk.unknown.ac.sessions > 0 ? [("Other", pk.unknown.ac.cost)] : [])
            } else {
                buckets = [("AC", e.byKind.ac.cost), ("Fast charging", e.byKind.dcfc.cost)]
            }
            stats = buckets.map { Stat(label: $0.0, value: L.money($0.1, cur)) }
            let est = L.estimatedShare(m)
            if est > 0 { notes.append("Includes \(Int((est * 100).rounded()))% estimated from network rates") }
            let n = m.spend.unpricedSessions + (m.spend.estimatePendingSessions ?? 0)
            if n > 0 { notes.append("+ \(n) \(L.plural(n, "session", "sessions")) not yet priced") }
        }
        if let pct = offpeakSharePct, pct.isFinite {
            stats.append(Stat(label: "Home charging off-peak", value: "\(Int(min(100, max(0, pct)).rounded()))%"))
        }
        let eyebrow = L.monthTitle(m.period, calendar: calendar) + (m.isCurrentMonth ? " so far" : "")
        return MoneyShareCard(eyebrow: eyebrow, title: title, subtitle: subtitle, stats: stats, notes: notes)
    }
}

/// The card painted as the web paints it: a fixed 1080 x 1080 dark image in the system font, the same positions and
/// sizes as `paintShareCard` (pixels of the exported image, not the app's type scale).
struct MoneyShareCardCanvas: View {
    let card: MoneyShareCard

    static let side: CGFloat = 1080
    private static let column: CGFloat = side - 160

    var body: some View {
        Canvas { ctx, _ in
            let w = Self.side
            ctx.fill(Path(CGRect(x: 0, y: 0, width: w, height: w)), with: .color(Theme.shareCardBackground))
            Self.text(ctx, card.eyebrow, size: 44, weight: .medium, color: Theme.shareCardMuted, x: 80, baseline: 140)
            var size: CGFloat = 190
            while size > 64 && Self.width(ctx, card.title, size: size, weight: .bold) > Self.column { size -= 8 }
            // A card with no figures (the milestone) is a headline and a sentence: centre that block between the label
            // and the footer rather than leave the lower half empty.
            var y: CGFloat = card.stats.isEmpty && card.notes.isEmpty ? 480 : 330
            Self.text(ctx, card.title, size: size, weight: .bold, color: Theme.shareCardTitle, x: 80, baseline: y)
            y = Self.wrap(ctx, card.subtitle, size: 44, color: Theme.shareCardMuted, x: 80, baseline: y + 80, lineHeight: 56, maxLines: 3)
            y += 30
            let rowSize: CGFloat = card.compact ? 40 : 44
            for s in card.stats.prefix(6) {
                Self.text(ctx, s.label, size: rowSize, weight: .medium, color: Theme.shareCardMuted, x: 80, baseline: y)
                Self.text(ctx, s.value, size: rowSize, weight: .medium, color: Theme.shareCardValue, x: w - 80, baseline: y, trailing: true)
                y += card.compact ? 66 : 78
            }
            for note in card.notes {
                Self.text(ctx, note, size: 32, weight: .regular, color: Theme.shareCardNote, x: 80, baseline: y)
                y += 44
            }
            var fy = w - 70 - CGFloat(MoneyShareCard.footer.count - 1) * 44
            for line in MoneyShareCard.footer {
                Self.text(ctx, line, size: 32, weight: .regular, color: Theme.shareCardFooter, x: 80, baseline: fy)
                fy += 44
            }
        }
        .frame(width: Self.side, height: Self.side)
        .environment(\.colorScheme, .dark)
        .accessibilityHidden(true)
    }

    private static func resolved(_ ctx: GraphicsContext, _ s: String, size: CGFloat, weight: Font.Weight, color: Color) -> GraphicsContext.ResolvedText {
        ctx.resolve(Text(s).font(.system(size: size, weight: weight)).foregroundColor(color))
    }

    private static func width(_ ctx: GraphicsContext, _ s: String, size: CGFloat, weight: Font.Weight) -> CGFloat {
        resolved(ctx, s, size: size, weight: weight, color: .white).measure(in: CGSize(width: CGFloat.infinity, height: .infinity)).width
    }

    /// Draws a line with its baseline at `baseline`, left-aligned at `x` or right-aligned to it.
    private static func text(_ ctx: GraphicsContext, _ s: String, size: CGFloat, weight: Font.Weight, color: Color, x: CGFloat, baseline: CGFloat,
                             trailing: Bool = false) {
        let r = resolved(ctx, s, size: size, weight: weight, color: color)
        let m = r.measure(in: CGSize(width: CGFloat.infinity, height: .infinity))
        let top = baseline - r.firstBaseline(in: m)
        ctx.draw(r, at: CGPoint(x: trailing ? x - m.width : x, y: top), anchor: .topLeading)
    }

    /// Wraps words to the column (the web's `wrap`); returns the baseline after the last line.
    private static func wrap(_ ctx: GraphicsContext, _ s: String, size: CGFloat, color: Color, x: CGFloat, baseline: CGFloat,
                             lineHeight: CGFloat, maxLines: Int) -> CGFloat {
        var y = baseline
        var line = ""
        var lines = 0
        for word in s.split(whereSeparator: \.isWhitespace).map(String.init) {
            let test = line.isEmpty ? word : "\(line) \(word)"
            if !line.isEmpty && width(ctx, test, size: size, weight: .regular) > column {
                text(ctx, line, size: size, weight: .regular, color: color, x: x, baseline: y)
                y += lineHeight
                lines += 1
                line = word
                if lines >= maxLines { return y }
            } else {
                line = test
            }
        }
        if !line.isEmpty {
            text(ctx, line, size: size, weight: .regular, color: color, x: x, baseline: y)
            y += lineHeight
        }
        return y
    }

    /// The card as a PNG, made on the phone.
    @MainActor
    static func png(_ card: MoneyShareCard) -> Data? {
        let r = ImageRenderer(content: MoneyShareCardCanvas(card: card))
        r.scale = 1
        return r.uiImage?.pngData()
    }
}

/// A PNG handed to the share sheet under the card's generic file name.
struct MoneySharePNG: Transferable {
    let data: Data
    let name: String

    static var transferRepresentation: some TransferRepresentation {
        DataRepresentation(exportedContentType: .png) { $0.data }
            .suggestedFileName { $0.name }
    }
}

/// "Share a card": the preview, the cost toggle and the off-peak toggle (both off every time it opens, never
/// remembered), and Share through the iOS share sheet (which also saves or copies the image). Made on the phone;
/// nothing is uploaded or posted.
struct MoneyShareSheet: View {
    let month: LedgerMonth
    /// The off-peak share for this month, or nil when there is none (then no toggle).
    let loadOffpeak: () async -> Double?
    var calendar: Calendar = .current
    @Environment(\.dismiss) private var dismiss

    @State private var includeCost = false
    @State private var includeOffpeak = false
    @State private var offpeak: Double?
    @State private var png: Data?

    private var card: MoneyShareCard {
        MoneyShareCard.month(month, includeCost: includeCost, offpeakSharePct: includeOffpeak ? offpeak : nil, calendar: calendar)
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    Group {
                        if let png, let image = UIImage(data: png) {
                            Image(uiImage: image).resizable().aspectRatio(1, contentMode: .fit)
                        } else {
                            Theme.shareCardBackground.aspectRatio(1, contentMode: .fit)
                        }
                    }
                    .clipShape(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
                    .accessibilityElement()
                    .accessibilityLabel("Preview of the card: \(([card.eyebrow, card.title, card.subtitle] + card.stats.map { "\($0.label) \($0.value)" } + card.notes).joined(separator: ", "))")

                    toggle("Include cost", "Adds charging cost and cost per mile.", $includeCost)
                    if offpeak != nil {
                        toggle("Include off-peak share", "Adds the percentage of home charging that ran in your off-peak hours. No rates, no dollars, no times.", $includeOffpeak)
                    }
                    Text("Made on your device. Nothing is uploaded or posted.").themeText(.subline, color: Theme.textTertiary)
                    if let png {
                        ShareLink(item: MoneySharePNG(data: png, name: MoneyShareCard.fileName(month)),
                                  preview: SharePreview("Rivolt", image: Image(uiImage: UIImage(data: png) ?? UIImage()))) {
                            Text("Share")
                        }
                        .buttonStyle(ThemeButtonStyle())
                    }
                }
                .padding(4 * Theme.unit)
            }
            .background(ThemePageBackground().ignoresSafeArea())
            .navigationTitle("Share a card")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            }
            .task { offpeak = await loadOffpeak() }
            .task(id: card) { png = MoneyShareCardCanvas.png(card) }
        }
    }

    private func toggle(_ title: String, _ hint: String, _ on: Binding<Bool>) -> some View {
        Toggle(isOn: on) {
            VStack(alignment: .leading, spacing: 2) {
                Text(title).themeText(.value, color: Theme.textPrimary)
                Text(hint).themeText(.subline).fixedSize(horizontal: false, vertical: true)
            }
        }
        .tint(Theme.accentButton)
    }
}

/// A file for the iOS share sheet, in its own temporary folder, removed when the sheet closes.
struct MoneyExportFile: Identifiable {
    let url: URL
    var id: URL { url }

    /// Writes the CSV as "rivolt-statement-2026-10.csv", the web's download name.
    static func csv(_ data: Data, month: String) throws -> MoneyExportFile {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("money-export-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let url = dir.appendingPathComponent("rivolt-statement-\(month).csv")
        try data.write(to: url, options: [.atomic, .completeFileProtection])
        return MoneyExportFile(url: url)
    }

    func remove() {
        try? FileManager.default.removeItem(at: url.deletingLastPathComponent())
    }
}

/// The system share sheet for a file (Save to Files, AirDrop, Mail, Numbers…).
struct MoneyActivitySheet: UIViewControllerRepresentable {
    let file: MoneyExportFile
    let onDone: () -> Void

    func makeUIViewController(context: Context) -> UIActivityViewController {
        let vc = UIActivityViewController(activityItems: [file.url], applicationActivities: nil)
        vc.completionWithItemsHandler = { _, _, _, _ in onDone() }
        return vc
    }

    func updateUIViewController(_ vc: UIActivityViewController, context: Context) {}
}
