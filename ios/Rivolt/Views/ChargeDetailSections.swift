import SwiftUI

/// What follows the tiles on a charge's detail, as the web's charge page: the charging curve, the session's numbers
/// and what the charge cost. One read of the charge's samples feeds the first two; the third is the charge's own row.
struct ChargeDetailSections: View {
    let charge: ActivityCharge
    var calendar: Calendar = .current

    @Environment(AppState.self) private var state
    @State private var model = ChargeCurveModel()

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            ChargeCurveCard(phase: model.phase, charge: charge, calendar: calendar)
            ChargeSessionCard(charge: charge, insights: model.insights ?? ChargeInsights.compute(charge, samples: []))
            ChargePricingCard(charge: charge, serverURL: state.serverURL)
        }
        .task(id: charge.id) { await model.load(source: state.client, charge: charge) }
    }
}
