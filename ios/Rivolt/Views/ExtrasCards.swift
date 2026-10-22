import SwiftUI

/// A milestone the truck just reached: the server's title and body as given, one at a time, newest first.
struct MilestoneCard: View {
    let milestone: Milestone
    let failed: Bool
    let onHide: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            VStack(alignment: .leading, spacing: 4) {
                Text(verbatim: milestone.title).font(Theme.sans(ThemeTokens.typeValue, .semibold, relativeTo: .headline))
                    .foregroundStyle(Theme.textPrimary).fixedSize(horizontal: false, vertical: true)
                    .accessibilityAddTraits(.isHeader)
                Text(verbatim: milestone.body).themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
                if failed {
                    Text("Couldn't hide that just now. Try again.").themeText(.subline, color: Theme.estimate)
                }
            }
            Spacer(minLength: 8)
            Button("Hide this", action: onHide)
                .font(Theme.sans(ThemeTokens.typeSubline, .regular, relativeTo: .footnote))
                .foregroundStyle(Theme.textSecondary)
                .accessibilityLabel("Hide this milestone")
        }
        .themeCard(padding: 4 * Theme.unit)
    }
}

/// "Three things you may not have turned on": each row only while that thing is off, with where to turn it on. The
/// notification row opens the phone's own notification settings; the lease and statement rows open the web settings.
struct ThreeThingsCard: View {
    let rows: [ThreeThingsRow]
    let webURL: (ThreeThingsRow) -> URL?
    let onOpenNotifications: () -> Void
    let onHide: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .top, spacing: 12) {
                Text("Three things you may not have turned on")
                    .font(Theme.sans(ThemeTokens.typeValue, .semibold, relativeTo: .headline))
                    .foregroundStyle(Theme.textPrimary)
                    .fixedSize(horizontal: false, vertical: true)
                    .accessibilityAddTraits(.isHeader)
                Spacer(minLength: 8)
                Button("Hide this", action: onHide)
                    .font(Theme.sans(ThemeTokens.typeSubline, .regular, relativeTo: .footnote))
                    .foregroundStyle(Theme.textSecondary)
                    .accessibilityLabel("Hide this card")
            }
            ForEach(rows) { row in
                // The text on the left (it wraps) and the action on the right, as the web's; at accessibility sizes the
                // action drops below.
                let layout = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(alignment: .center, spacing: 12))
                layout {
                    text(row)
                    action(for: row)
                }
                .padding(3 * Theme.unit)
                .frame(maxWidth: .infinity, alignment: .leading)
                .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
            }
        }
        .themeCard(padding: 4 * Theme.unit)
    }

    private func text(_ row: ThreeThingsRow) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(row.label).themeText(.value, color: Theme.textPrimary)
            // Wrap to the full hint at any text size, never cut it off with an ellipsis.
            Text(row.hint).themeText(.subline).fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder
    private func action(for row: ThreeThingsRow) -> some View {
        if row == .push {
            Button(row.cta, action: onOpenNotifications).buttonStyle(ThemeSecondaryButtonStyle()).fixedSize()
        } else if let url = webURL(row) {
            Link(destination: url) { Text(row.cta) }.buttonStyle(ThemeSecondaryButtonStyle()).fixedSize()
        }
    }
}
