import SwiftUI

/// The web's segmented filter: a bordered, rounded container with the selected option on a raised cell.
struct ThemeSegmented<ID: Hashable>: View {
    struct Option: Identifiable {
        let id: ID
        let label: String
    }

    let options: [Option]
    @Binding var selection: ID
    /// What the choice is about, read before each option by VoiceOver ("View, Map").
    var accessibilityName: String?
    /// Options share the full width (a row of its own) instead of hugging their labels; at accessibility text sizes
    /// they stack, so a label never breaks inside a word.
    var fillsWidth = false

    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let layout = fillsWidth && typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(spacing: 2)) : AnyLayout(HStackLayout(spacing: 2))
        layout {
            ForEach(options) { o in
                let on = o.id == selection
                Button { selection = o.id } label: {
                    Text(o.label)
                        .font(Theme.sans(ThemeTokens.typeValue, on ? .medium : .regular, relativeTo: .body))
                        .lineLimit(1)
                        .minimumScaleFactor(0.8)
                        .foregroundStyle(on ? Theme.textPrimary : Theme.textSecondary)
                        .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 2 * Theme.unit)
                        .frame(maxWidth: fillsWidth ? .infinity : nil)
                        .background(on ? Theme.border : .clear, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel(accessibilityName.map { "\($0), \(o.label)" } ?? o.label)
                .accessibilityAddTraits(on ? [.isSelected, .isButton] : .isButton)
            }
        }
        .padding(Theme.unit)
        .background(Theme.card.opacity(0.5), in: RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusCard, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
    }
}
