import SwiftUI

/// What follows the tiles on a charge's detail, as the web's charge page: the charging curve, the session's numbers
/// and what the charge cost. One read of the charge's samples feeds the first two; the third is the charge's own row.
struct ChargeDetailSections: View {
    let charge: ActivityCharge
    /// The charge's row once the screen has read it (shared with the map).
    let row: ChargeRow?
    var calendar: Calendar = .current

    @Environment(AppState.self) private var state
    @State private var model = ChargeCurveModel()

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            ChargeCurveCard(phase: model.phase, charge: charge, calendar: calendar)
            ChargeSessionCard(charge: charge, insights: ChargeInsights.compute(charge, samples: model.samples, row: row))
            ChargePricingCard(charge: charge, serverURL: state.serverURL)
        }
        .task(id: charge.id) { await model.load(source: state.client, charge: charge) }
    }
}
