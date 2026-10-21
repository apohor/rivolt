import SwiftUI
import WidgetKit

@main
struct RivoltWidgets: WidgetBundle {
    var body: some Widget {
        NowWidget()
    }
}

/// Charge and range on the home screen and the lock screen. Read-only.
struct NowWidget: Widget {
    let kind = "dev.rivolt.widget.now"

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: NowProvider()) { entry in
            NowWidgetEntryView(entry: entry)
                .containerBackground(.fill.tertiary, for: .widget)
        }
        .configurationDisplayName("Charge and range")
        .description("Your truck's charge level and range.")
        .supportedFamilies([.systemSmall, .systemMedium, .accessoryCircular, .accessoryRectangular, .accessoryInline])
    }
}

struct NowProvider: TimelineProvider {
    func placeholder(in context: Context) -> NowEntry {
        NowEntry(date: .now, content: .data(.sample))
    }

    func getSnapshot(in context: Context, completion: @escaping (NowEntry) -> Void) {
        // The gallery shows made-up numbers; anywhere else, what was last known.
        if context.isPreview {
            completion(NowEntry(date: .now, content: .data(.sample)))
        } else {
            let content = WidgetStore().snapshot.map(WidgetContent.data) ?? .unavailable
            completion(NowEntry(date: .now, content: content))
        }
    }

    func getTimeline(in context: Context, completion: @escaping (Timeline<NowEntry>) -> Void) {
        Task {
            let store = WidgetStore()
            let now = Date()
            let outcome = await WidgetFetch.load(store: store, clock: now)
            let content = WidgetResolver.resolve(outcome, store: store)
            completion(Timeline(entries: WidgetTimelineBuilder.entries(content, at: now),
                                policy: .after(WidgetTimelineBuilder.nextRefresh(content, at: now))))
        }
    }
}
