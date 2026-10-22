import SwiftUI
import WidgetKit

@main
struct RivoltWidgets: WidgetBundle {
    /// Geist, bundled in the extension too (OFL.txt ships with it).
    init() { Theme.registerFonts() }

    var body: some Widget {
        NowWidget()
        ChargingLiveActivityWidget()
    }
}

/// The charging Live Activity on the Lock Screen and in the Dynamic Island. Read-only; a tap opens the app.
struct ChargingLiveActivityWidget: Widget {
    var body: some WidgetConfiguration {
        ActivityConfiguration(for: ChargingActivityAttributes.self) { context in
            ChargingLockScreenView(state: context.state, targetPct: context.attributes.targetPct, isStale: context.isStale, isTest: context.attributes.isTest)
                // The Theme's page colour under the card (the system adds its own material and corners).
                .activityBackgroundTint(Theme.background)
                .activitySystemActionForegroundColor(Theme.accent)
        } dynamicIsland: { context in
            DynamicIsland {
                DynamicIslandExpandedRegion(.leading) {
                    ChargingIslandLeading(state: context.state, targetPct: context.attributes.targetPct, isStale: context.isStale)
                }
                DynamicIslandExpandedRegion(.trailing) {
                    ChargingIslandTrailing(state: context.state, isStale: context.isStale)
                }
                DynamicIslandExpandedRegion(.bottom) {
                    ChargingIslandBottom(state: context.state, targetPct: context.attributes.targetPct)
                }
            } compactLeading: {
                ChargingIslandIcon(isStale: context.isStale)
            } compactTrailing: {
                ChargingIslandLevel(state: context.state)
            } minimal: {
                ChargingIslandIcon(isStale: context.isStale)
            }
            .keylineTint(Theme.accent)
        }
    }
}

/// Charge and range on the home screen and the lock screen. Read-only.
struct NowWidget: Widget {
    let kind = "dev.rivolt.widget.now"

    var body: some WidgetConfiguration {
        StaticConfiguration(kind: kind, provider: NowProvider()) { entry in
            NowWidgetEntryView(entry: entry)
                // The web's Now hero on the home screen; the lock screen draws its own (tinted) background.
                .containerBackground(for: .widget) { NowWidgetBackground() }
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
