import SwiftUI

/// A milestone the truck just reached: the server's title and body as given, one at a time, newest first.
struct MilestoneCard: View {
    let milestone: Milestone
    let failed: Bool
    let onHide: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(verbatim: milestone.title).font(.headline).accessibilityAddTraits(.isHeader)
            Text(verbatim: milestone.body).font(.subheadline).foregroundStyle(NowInk.secondary).fixedSize(horizontal: false, vertical: true)
            if failed {
                Text("Couldn't hide that just now. Try again.").font(.footnote).foregroundStyle(NowInk.stale)
            }
            Button("Hide this", action: onHide)
                .font(.footnote)
                .accessibilityLabel("Hide this milestone")
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(20)
        .background(.background, in: RoundedRectangle(cornerRadius: 20))
    }
}

/// "Three things you may not have turned on": each row only while that thing is off, with where to turn it on. The
/// notification row opens the phone's own notification settings; the lease and statement rows open the web settings.
struct ThreeThingsCard: View {
    let rows: [ThreeThingsRow]
    let webURL: (ThreeThingsRow) -> URL?
    let onOpenNotifications: () -> Void
    let onHide: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .top) {
                Text("Three things you may not have turned on").font(.headline).fixedSize(horizontal: false, vertical: true).accessibilityAddTraits(.isHeader)
                Spacer(minLength: 8)
                Button("Hide this", action: onHide).font(.footnote)
                    .accessibilityLabel("Hide this card")
            }
            ForEach(rows) { row in
                VStack(alignment: .leading, spacing: 6) {
                    Text(row.label).font(.subheadline.weight(.medium))
                    // Wrap to the full hint at any text size, never cut it off with an ellipsis.
                    Text(row.hint).font(.footnote).foregroundStyle(NowInk.secondary).fixedSize(horizontal: false, vertical: true)
                    action(for: row)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(12)
                .overlay(RoundedRectangle(cornerRadius: 12).stroke(.quaternary))
            }
        }
        .padding(20)
        .background(.background, in: RoundedRectangle(cornerRadius: 20))
    }

    @ViewBuilder
    private func action(for row: ThreeThingsRow) -> some View {
        if row == .push {
            Button(row.cta, action: onOpenNotifications).buttonStyle(.bordered)
        } else if let url = webURL(row) {
            Link(row.cta, destination: url).buttonStyle(.bordered)
        }
    }
}
