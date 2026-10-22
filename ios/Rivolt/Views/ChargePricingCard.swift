import SwiftUI

/// The wording of the pricing summary: what the charge cost and where the price came from, in the ledger's words.
enum ChargePricing {
    struct Summary: Equatable {
        let cost: ChargeCostLine
        let rate: String?
        let source: String
        let network: String?
        /// One sentence on what to do, or nil when the price is a settled one.
        let advice: String?
    }

    static func summary(_ c: ActivityCharge) -> Summary {
        let line = ActivityLogic.costLine(c)
        let network = c.network.flatMap { $0.isEmpty ? nil : $0 }
        let advice: String?
        switch line.tone {
        case .missing: advice = "This charge has no price yet."
        case .estimate: advice = "This is an estimate. Confirm the network or enter the real price on the web."
        case .billed, .free: advice = nil
        }
        return Summary(cost: line, rate: ChargeInsights.costPerKwh(c), source: source(c), network: network, advice: advice)
    }

    /// Where the price came from, as the Money receipts say it.
    static func source(_ c: ActivityCharge) -> String {
        switch c.costSourceEffective ?? "" {
        case "rivian": return "Billed"
        case "home_rate": return c.costSourceDerived == true ? "Home rate (worked out, not recorded)" : "Home rate"
        case "network_estimate": return "Estimate"
        case "observed_estimate": return "Estimate from typical observed prices"
        case "user": return c.cost == 0 ? "Free · entered by you" : "Entered by you"
        case "home_rate_misapplied": return "Estimate pending · a fast charge was priced at your home rate"
        default: return "Price unknown"
        }
    }
}

/// A read-only summary of the charge's price, with a link to edit it on the web (the app doesn't change prices).
struct ChargePricingCard: View {
    let charge: ActivityCharge
    /// The user's own Rivolt server: the page that edits the price.
    let serverURL: URL

    private var summary: ChargePricing.Summary { ChargePricing.summary(charge) }

    private var editURL: URL? {
        URL(string: MoneyLink.charge(charge.id, pricing: true).path, relativeTo: serverURL)?.absoluteURL
    }

    private func tone(_ t: ChargeCostLine.Tone) -> Color {
        switch t {
        case .missing: return Theme.estimate
        case .estimate: return Theme.textBody
        case .free, .billed: return Theme.textPrimary
        }
    }

    var body: some View {
        let s = summary
        ThemeSection(title: "Pricing") {
            VStack(alignment: .leading, spacing: 12) {
                row("Cost", s.cost.text, color: tone(s.cost.tone))
                if let rate = s.rate { row("Price per kWh", rate) }
                row("Source", s.source + (s.network.map { " · \($0)" } ?? ""))
                if let advice = s.advice { Text(advice).themeText(.subline, color: Theme.textTertiary) }
                if let u = editURL {
                    Link(destination: u) { Text("Edit pricing on the web ↗").themeText(.subline, color: Theme.accent).fontWeight(.medium) }
                        .accessibilityLabel("Edit pricing on the web")
                }
            }
            .padding(4 * Theme.unit)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func row(_ label: String, _ value: String, color: Color = Theme.textPrimary) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label).themeText(.label)
            Text(value).themeText(.value, color: color)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
        .accessibilityValue(SpokenText.words(value.replacingOccurrences(of: "/kWh", with: " per kilowatt hour")))
    }
}
