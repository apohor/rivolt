import SwiftUI

/// Items laid out left to right, wrapping to a new row when the next one does not fit: no item is clipped or hidden behind a scroll, at any text size.
struct FlowLayout: Layout {
    var spacing: CGFloat = 8

    /// Which items go on which row, for item widths and the room there is. An item wider than the room still gets a row of its own.
    static func rows(widths: [CGFloat], maxWidth: CGFloat, spacing: CGFloat) -> [[Int]] {
        var rows: [[Int]] = [[]]
        var used: CGFloat = 0
        for (i, w) in widths.enumerated() {
            let need = rows[rows.count - 1].isEmpty ? w : used + spacing + w
            if need > maxWidth, !rows[rows.count - 1].isEmpty {
                rows.append([i])
                used = w
            } else {
                rows[rows.count - 1].append(i)
                used = need
            }
        }
        return rows.filter { !$0.isEmpty }
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let room = proposal.width ?? .infinity
        let sizes: [CGSize] = subviews.map { $0.sizeThatFits(ProposedViewSize(width: room, height: nil)) }
        let widths: [CGFloat] = sizes.map { $0.width }
        let rows = Self.rows(widths: widths, maxWidth: room, spacing: spacing)
        var height: CGFloat = 0
        var width: CGFloat = 0
        for row in rows {
            var rowHeight: CGFloat = 0
            var rowWidth: CGFloat = 0
            for i in row {
                rowHeight = max(rowHeight, sizes[i].height)
                rowWidth += sizes[i].width
            }
            rowWidth += spacing * CGFloat(max(0, row.count - 1))
            height += rowHeight
            width = max(width, rowWidth)
        }
        height += spacing * CGFloat(max(0, rows.count - 1))
        return CGSize(width: min(width, room), height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        // Each is measured with the room there is, so a label that would not fit even alone on a row wraps its text instead of running off the edge.
        let sizes: [CGSize] = subviews.map { $0.sizeThatFits(ProposedViewSize(width: bounds.width, height: nil)) }
        let widths: [CGFloat] = sizes.map { $0.width }
        let rows = Self.rows(widths: widths, maxWidth: bounds.width, spacing: spacing)
        var y = bounds.minY
        for row in rows {
            var x = bounds.minX
            let h = row.map { sizes[$0].height }.max() ?? 0
            for i in row {
                subviews[i].place(at: CGPoint(x: x, y: y + (h - sizes[i].height) / 2), proposal: ProposedViewSize(sizes[i]))
                x += sizes[i].width + spacing
            }
            y += h + spacing
        }
    }
}
