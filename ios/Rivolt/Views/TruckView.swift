import Charts
import SwiftUI

/// Truck: the truck's health from its own history (the web's Truck page): parked drain over 7 and 30 days, the lease,
/// pack capacity, trends, range at different temperatures, sleep and idle-awake for the month, and tires and software.
/// Read-only: the lease and everything else are set on the web. Nothing here carries a location.
struct TruckView: View {
    @Environment(AppState.self) private var state

    @AppStorage("rivolt.selected_vehicle") private var selectedID = ""
    @AppStorage("rivolt.r2.checked.dismissed") private var r2Dismissed = false
    @State private var vehicles: [Vehicle] = []
    @State private var vehiclesLoaded = false
    @State private var vehiclesError: String?
    @State private var model = TruckModel()
    @State private var month = PeriodMonth(containing: .now, calendar: .current)
    @State private var drainKey: TruckLogic.DrainKey = .week
    @State private var pickedTemp: Double?
    @State private var autoTemp: Double?

    private var selected: Vehicle? { vehicles.first { $0.id == selectedID } ?? vehicles.first }
    private var metric: Bool { NowFormat.usesMetric }
    private var fahrenheit: Bool { !metric }
    private var temp: Double { pickedTemp ?? autoTemp ?? TruckLogic.grid(fahrenheit: fahrenheit)[2] }

    var body: some View {
        NavigationStack {
            ScrollViewReader { proxy in
            ScrollView {
                Group {
                    if let v = selected {
                        TruckContent(model: model, vehicle: v, drainKey: $drainKey, temp: temp, pickTemp: { pickedTemp = $0 },
                                     r2Dismissed: $r2Dismissed, month: month,
                                     notesLink: { id, which in try await state.client.otaNotesURL(vehicleID: id, which: which) },
                                     showCard: { id in withAnimation { proxy.scrollTo(id, anchor: .top) } })
                    } else if let vehiclesError {
                        TruckMessage(text: vehiclesError)
                    } else if vehiclesLoaded {
                        TruckMessage(text: "Connect your Rivian to see pack health and sleep here.")
                    } else {
                        ProgressView().controlSize(.large).padding(.top, 80)
                    }
                }
                .padding(.horizontal, 4 * Theme.unit)
                .padding(.vertical, 4 * Theme.unit)
            }
            .task {
                // App Store screenshots only: bring the sleep and tires cards to the top once the screen has loaded.
                guard StoreShots.name == "truck" else { return }
                try? await Task.sleep(nanoseconds: 25_000_000_000)
                proxy.scrollTo("sleep", anchor: .top)
            }
            }
            .background(ThemePageBackground().ignoresSafeArea())
            .toolbar(.hidden, for: .navigationBar)
            .refreshable { await reload() }
            .task { await loadVehicles() }
            .task(id: selected?.id) {
                guard let id = selected?.id else { return }
                pickedTemp = nil
                autoTemp = nil
                await model.load(source: state.client, vehicleID: id, metric: metric)
            }
            .task(id: "\(selected?.id ?? "")|\(month.start.timeIntervalSince1970)") {
                guard let id = selected?.id else { return }
                await model.loadSleep(source: state.client, vehicleID: id, month: month)
            }
            .task(id: "\(selected?.id ?? "")|\(temp)") {
                guard let id = selected?.id else { return }
                await model.loadCold(source: state.client, vehicleID: id, tempC: temp, fahrenheit: fahrenheit, metric: metric)
                // Until a chip is picked: the coldest one with enough history (the winter question).
                if pickedTemp == nil, autoTemp == nil, let a = model.cold?.value?.available.min() { autoTemp = a }
            }
            .safeAreaInset(edge: .top, spacing: 0) {
                VStack(spacing: 0) {
                    ThemeTopBar { AccountMenu(vehicleID: selected?.id, onRefresh: { Task { await reload() } }) }
                    titleRow
                }
            }
        }
    }

    private var titleRow: some View {
        ThemeTitleRow(title: "Truck") {
            if vehicles.count > 1 {
                Menu {
                    Picker("Vehicle", selection: $selectedID) {
                        ForEach(vehicles) { v in Text(v.name).tag(v.id) }
                    }
                } label: {
                    Image(systemName: "car.2").font(.body).foregroundStyle(Theme.textSecondary)
                        .frame(width: 36, height: 36)
                }
                .accessibilityLabel("Switch vehicle")
            }
            ThemeStepper(title: month.title(calendar: .current), spokenTitle: month.spokenTitle(calendar: .current), canGoForward: !month.isCurrent(now: .now, calendar: .current),
                         previous: { month = month.shifted(by: -1, calendar: .current) },
                         next: { month = month.shifted(by: 1, calendar: .current) })
        }
    }

    private func loadVehicles() async {
        do {
            vehicles = try await state.client.vehicles()
            if selected == nil || !vehicles.contains(where: { $0.id == selectedID }) {
                selectedID = vehicles.first?.id ?? ""
            }
            vehiclesError = nil
        } catch {
            if vehicles.isEmpty { vehiclesError = UserFacingError.words(error) }
        }
        vehiclesLoaded = true
    }

    private func reload() async {
        await loadVehicles()
        guard let id = selected?.id else { return }
        async let a: Void = model.load(source: state.client, vehicleID: id, metric: metric, force: true)
        async let b: Void = model.loadSleep(source: state.client, vehicleID: id, month: month, force: true)
        async let c: Void = model.loadCold(source: state.client, vehicleID: id, tempC: temp, fahrenheit: fahrenheit, metric: metric, force: true)
        _ = await (a, b, c)
    }
}

private struct TruckMessage: View {
    let text: String

    var body: some View {
        Text(text).themeText(.value, color: Theme.textSecondary).multilineTextAlignment(.center)
            .frame(maxWidth: .infinity)
            .padding(.top, 80)
    }
}

/// The cards, in the web's order, drawn from the model alone (so tests can render them from fixtures).
struct TruckContent: View {
    let model: TruckModel
    let vehicle: Vehicle
    @Binding var drainKey: TruckLogic.DrainKey
    let temp: Double
    let pickTemp: (Double) -> Void
    @Binding var r2Dismissed: Bool
    let month: PeriodMonth
    var now: Date = .now
    var calendar: Calendar = .current
    var metric: Bool = NowFormat.usesMetric
    /// A fresh release-notes link from the server (`GET /api/vehicles/{id}/ota/notes`); tests render without one.
    var notesLink: (_ vehicleID: String, _ which: String) async throws -> URL = { _, _ in throw APIError.badURL }
    /// Scrolls to a card by its id ("pack", "drain"); the summary line's halves call it.
    var showCard: ((String) -> Void)? = nil
    @Environment(\.dynamicTypeSize) private var typeSize

    /// At an accessibility size pairs stack (label over value) and rows become a column, so nothing breaks mid-word.
    private var stacked: Bool { typeSize.isAccessibilitySize }
    private var chartHeight: CGFloat { stacked ? 200 : 140 }

    private var fahrenheit: Bool { !metric }

    var body: some View {
        VStack(spacing: 16) {
            // The first screen answers "how is the truck": the two figures from the cards below, above the fold.
            summaryLine
            if TruckLogic.isR2(vehicle.model) && !r2Dismissed { r2Card }
            if let d = model.drain?.value { drainCard(d).id("drain") } else if model.drain == nil { TruckLoading(title: "Parked drain") }
            if let terms = model.leaseTerms, let st = model.lease?.value { leaseCard(st, terms: terms) }
            packCard.id("pack")
            if let t = model.trends?.value { trendsCard(t) } else if model.trends == .failed { failedCard("Trends for your truck", "Couldn't load trends.") }
            if let c = model.cold?.value { coldCard(c) } else if model.cold == .failed { failedCard("Range at different temperatures", "Couldn't load this.") }
            placesCard
            sleepCard.id("sleep")
            tiresCard
            vehicleCard
        }
    }

    /// Each half a 44 pt target that shows its card; side by side when both fit whole, else one per row.
    @ViewBuilder
    private var summaryLine: some View {
        let parts = TruckLogic.summaryParts(pack: model.pack?.value, trends: model.trends?.value, drain: model.drain?.value)
        if !parts.isEmpty {
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .center, spacing: 0) {
                    ForEach(Array(parts.enumerated()), id: \.element.id) { i, p in
                        if i > 0 { Text(" · ").themeText(.value, color: Theme.textTertiary).accessibilityHidden(true) }
                        summaryButton(p, lowered: i > 0).fixedSize()
                    }
                }
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(parts) { summaryButton($0, lowered: false) }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .contain)
            .accessibilityLabel("Truck summary")
        }
    }

    private func summaryButton(_ p: TruckLogic.SummaryPart, lowered: Bool) -> some View {
        let text = lowered ? p.text.prefix(1).lowercased() + p.text.dropFirst() : p.text
        return Button { showCard?(p.id) } label: {
            Text(text).themeText(.value, color: Theme.textBody)
                .multilineTextAlignment(.leading)
                .fixedSize(horizontal: false, vertical: true)
                .frame(minHeight: 44, alignment: .leading)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(SpokenText.words(p.text))
        .accessibilityHint(p.id == "pack" ? "Shows Battery capacity" : "Shows Parked drain")
    }

    private func failedCard(_ title: String, _ text: String) -> some View {
        ThemeSection(title: title) { Text(text + " Pull down to try again.").themeText(.value, color: Theme.textSecondary) }
    }

    // MARK: R2

    private var r2Card: some View {
        ActionSection(title: "Your R2 in Rivolt: what's been checked") {
            Button { r2Dismissed = true } label: { Text("Got it").themeText(.subline) }.buttonStyle(.plain)
        } content: {
            VStack(alignment: .leading, spacing: 12) {
                Text("Rivolt supports the R2. Here is exactly what we've confirmed on real R2s, so you know what to trust.").themeText(.value)
                r2Block("Checked on real R2s", "drives · charges and plug-in · charge limit · battery, range and odometer · location · cabin and battery temperature · tire pressure · sleep and power state · reported battery capacity")
                r2Block("Shown as an estimate", "time to full charge (Rivian's own countdown; labelled \"estimate\"). Power state can take a minute to appear after Rivolt reconnects.")
                r2Block("Not on the R2 yet", "trip charge target · why a charge stopped · camp and pet modes · Rivian's parked-energy breakdown · smart-charging status. Each appears once we've confirmed it on an R2.")
                Text("Outside temperature doesn't come through for any truck; Rivolt can look it up from weather data if you turn that on (off by default).")
                    .themeText(.subline, color: Theme.textTertiary)
                if let u = URL(string: "https://github.com/apohor/rivolt/issues/new?template=r2-report.yml") {
                    Link(destination: u) { Text("Something looks wrong on your R2? Tell us").themeText(.value, color: Theme.accent).underline() }
                }
            }
            .fixedSize(horizontal: false, vertical: true)
        }
    }

    private func r2Block(_ title: String, _ text: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).themeText(.label)
            Text(text).themeText(.value)
        }
    }

    // MARK: Parked drain

    private func drainCard(_ d: DrainSnapshot) -> some View {
        let k = drainKey
        let v = TruckLogic.drainView(TruckLogic.window(d, k), k)
        let ov = TruckLogic.drainView(TruckLogic.window(d, k.other), k.other)
        return ActionSection(title: "Parked drain") {
            SmallSwitch(options: TruckLogic.DrainKey.allCases.map { ($0, $0.label) }, selection: $drainKey, label: "Window")
        } content: {
            VStack(alignment: .leading, spacing: 8) {
                switch v {
                case let .ok(p, kwh, sleeps, rewakes, median, used, excluded):
                    (stacked ? AnyLayout(VStackLayout(alignment: .leading, spacing: 4)) : AnyLayout(HStackLayout(alignment: .firstTextBaseline, spacing: 12))) {
                        (Text(TruckLogic.pct(p)).font(Theme.sans(ThemeTokens.typeHero, .semibold, relativeTo: .largeTitle)).foregroundStyle(Theme.textPrimary)
                            + Text(" % / day").font(Theme.sans(ThemeTokens.typeTitle - 4, .regular, relativeTo: .title2)).foregroundStyle(Theme.textTertiary))
                            .monospacedDigit()
                        if let kwh { Text("≈ \(TruckLogic.fixed(kwh, 1)) kWh / day").themeText(.value, color: Theme.textSecondary) }
                    }
                    .accessibilityElement(children: .combine)
                    Text(TruckLogic.drainSleeps(k, sleeps: sleeps, quickRewakes: rewakes, medianSleepSeconds: median)).themeText(.value)
                    Text(TruckLogic.drainBasis(daysUsed: used, daysExcluded: excluded, other: k.other, otherView: ov)).themeText(.subline, color: Theme.textTertiary)
                case let .insufficient(have, need):
                    Text("Not enough parked time yet to measure drain.").themeText(.value)
                    Text("\(TruckLogic.hours(have)) of parked data so far; the \(k == .week ? "7-day" : "30-day") figure needs \(need) h. It appears on its own as data comes in.")
                        .themeText(.subline, color: Theme.textTertiary)
                }
                if let split = TruckLogic.split(d, k) { splitView(split).padding(.top, 4) }
                if !d.daily.isEmpty { DrainBars(daily: d.daily).padding(.top, 8) }
                Text("Battery lost while parked and not charging. This shows what happened, not why.").themeText(.caption, color: Theme.textTertiary)
                    .padding(.top, 4)
            }
            .fixedSize(horizontal: false, vertical: true)
        }
    }

    @ViewBuilder
    private func splitView(_ s: TruckLogic.SplitView) -> some View {
        switch s {
        case .sparse:
            Text("Where the loss happened (asleep or awake) needs about two days of parked history with a few points lost.").themeText(.subline, color: Theme.textTertiary)
        case .gaps:
            Text("The power state wasn't recorded often enough to say when the loss happened.").themeText(.subline, color: Theme.textTertiary)
        case let .ok(text, share, awake, asleep):
            VStack(alignment: .leading, spacing: 6) {
                Text(text).themeText(.value)
                GeometryReader { g in
                    HStack(spacing: 0) {
                        if awake { Rectangle().fill(Theme.fast).frame(width: g.size.width * share) }
                        if asleep { Rectangle().fill(Theme.accentFill) }
                    }
                }
                .frame(height: 8)
                .background(Theme.border)
                .clipShape(Capsule())
                .accessibilityElement()
                .accessibilityLabel("\(Int((share * 100).rounded()))% of the loss was while awake, \(Int(((1 - share) * 100).rounded()))% while asleep")
                HStack(spacing: 12) {
                    if awake { legendDot(Theme.fast, "Awake") }
                    if asleep { legendDot(Theme.accentFill, "Asleep") }
                }
                Text("Estimate: the battery reports whole percents.").themeText(.caption, color: Theme.textTertiary)
            }
        }
    }

    private func legendDot(_ c: Color, _ label: String) -> some View {
        HStack(spacing: 4) {
            Circle().fill(c).frame(width: 8, height: 8)
            Text(label).themeText(.caption, color: Theme.textTertiary)
        }
    }

    // MARK: Lease

    private func leaseCard(_ st: LeaseStatus, terms: LeaseTerms) -> some View {
        let bar = TruckLogic.leaseBar(st, allowedTotal: terms.allowedMilesTotal)
        let fill = bar.tone == .over ? Theme.criticalBorder : bar.tone == .watch ? Theme.fastFill : Theme.accentFill
        let standing = TruckLogic.planStanding(st, metric: metric)
        let dist = { (mi: Double) in TruckLogic.distance(mi, metric: metric) }
        let unitWord = metric ? "kilometers" : "miles"
        return ThemeSection(title: "Lease") {
            VStack(alignment: .leading, spacing: 16) {
                if st.usedMi == nil {
                    Text("No recent odometer reading, so miles used and the projection are paused. They come back once the truck reports in.")
                        .themeText(.subline, color: Theme.textBody)
                        .padding(.horizontal, 3 * Theme.unit).padding(.vertical, 2 * Theme.unit)
                        .background(Theme.estimateFill.opacity(0.1), in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.estimateFill.opacity(0.4), lineWidth: 1))
                }
                (stacked ? AnyLayout(VStackLayout(alignment: .leading, spacing: 4)) : AnyLayout(HStackLayout(alignment: .lastTextBaseline))) {
                    (Text(st.usedMi.map { TruckLogic.whole(metric ? $0 * 1.609344 : $0) } ?? "—").font(Theme.sans(ThemeTokens.typeTitle, .semibold, relativeTo: .title)).foregroundStyle(Theme.textPrimary)
                        + Text(" of \(dist(terms.allowedMilesTotal))").font(Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body)).foregroundStyle(Theme.textSecondary))
                        .monospacedDigit()
                    if !stacked { Spacer(minLength: 8) }
                    if let standing {
                        Text(standing.text).themeText(.value, color: standing.tone == .watch ? Theme.estimate : Theme.accent).fontWeight(.medium)
                    }
                }
                VStack(alignment: .leading, spacing: 8) {
                    GeometryReader { g in
                        let w = g.size.width
                        ZStack(alignment: .leading) {
                            Capsule().fill(Theme.border)
                            if let p = bar.projected, p > bar.used { Capsule().fill(Theme.textTertiary.opacity(0.4)).frame(width: w * p) }
                            Capsule().fill(fill).frame(width: w * bar.used)
                            Rectangle().fill(Theme.textPrimary).frame(width: 2, height: 20).offset(x: w * bar.plan - 1)
                            if bar.allowed < 1 { Rectangle().fill(Theme.fast).frame(width: 2, height: 20).offset(x: w * bar.allowed - 1) }
                        }
                    }
                    .frame(height: 12)
                    .accessibilityElement()
                    .accessibilityLabel("Lease \(unitWord) used")
                    .accessibilityValue("\(dist(st.usedMi ?? 0)) used of \(dist(terms.allowedMilesTotal)); \(dist(st.allowedToDateMi)) allowed to date")
                    TruckFlow {
                        legendSwatch(fill, "Used")
                        legendTick(Theme.textPrimary, "Allowed to date · \(dist(st.allowedToDateMi))")
                        if bar.allowed < 1 { legendTick(Theme.fast, "Total allowed · \(dist(terms.allowedMilesTotal))") }
                        if bar.projected != nil { legendSwatch(Theme.textTertiary.opacity(0.5), "Projected at return") }
                    }
                }
                let returns = leaseFact("Returns", leaseDate(terms.endDate), ["\(st.daysLeft.formatted()) days left"])
                let projected = leaseFact("Projected at return", st.projectedTotalMi.map { "\(dist($0)) of lease \(unitWord)" } ?? "—",
                                          [st.projectedReturnOdometerMi.map { "odometer ~\(dist($0))" },
                                           st.paceMiPerDay.map { "at \(dist($0)) a day\(st.paceBasis == "last_90_days" ? " (last 90 days)" : st.paceBasis == "lease_to_date" ? " (since the lease began)" : "")" }].compactMap { $0 })
                let budget = leaseFact("\(metric ? "Kilometers" : "Miles") a day to stay within the allowance",
                                       st.milesPerDayBudget.map { "\(TruckLogic.fixed(metric ? $0 * 1.609344 : $0, 1)) \(metric ? "km" : "mi")" } ?? "—",
                                       [st.milesPerDayBudget == 0 ? "already over the allowance" : "from today to the end of the lease"])
                if stacked {
                    VStack(alignment: .leading, spacing: 12) {
                        returns
                        projected
                        overage(st, currency: terms.currency)
                        budget
                    }
                } else {
                    Grid(alignment: .topLeading, horizontalSpacing: 16, verticalSpacing: 12) {
                        GridRow {
                            returns
                            projected
                        }
                        GridRow {
                            overage(st, currency: terms.currency)
                            budget
                        }
                    }
                }
            }
        }
    }

    private func leaseDate(_ ymd: String) -> String {
        guard let d = JSON.parseDate("\(ymd)T12:00:00Z") else { return ymd }
        return d.formatted(Date.FormatStyle(timeZone: TimeZone(identifier: "UTC")!).month(.abbreviated).day().year())
    }

    private func leaseFact(_ label: String, _ value: String, _ notes: [String]) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label).themeText(.label)
            Text(value).themeText(.value, color: Theme.textPrimary).fontWeight(.semibold)
            ForEach(notes, id: \.self) { Text($0).themeText(.subline, color: Theme.textTertiary) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder
    private func overage(_ st: LeaseStatus, currency: String) -> some View {
        if let over = st.projectedOverageMi, over > 0 {
            leaseFact("Overage", "~\(MoneyLogic.money(st.projectedOverageCost ?? 0, currency))", ["\(TruckLogic.distance(over, metric: metric)) over"])
        } else if st.projectedOverageMi == nil {
            leaseFact("Overage", "—", [])
        } else {
            VStack(alignment: .leading, spacing: 2) {
                Text("Overage").themeText(.label)
                Text("None projected").themeText(.value, color: Theme.accent).fontWeight(.semibold)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private func legendSwatch(_ c: Color, _ label: String) -> some View {
        HStack(spacing: 4) {
            RoundedRectangle(cornerRadius: 2).fill(c).frame(width: 8, height: 8)
            Text(label).themeText(.caption, color: Theme.textTertiary)
        }
    }

    private func legendTick(_ c: Color, _ label: String) -> some View {
        HStack(spacing: 4) {
            Rectangle().fill(c).frame(width: 2, height: 10)
            Text(label).themeText(.caption, color: Theme.textTertiary)
        }
    }

    // MARK: Battery capacity

    /// "Battery capacity": what the truck reports (or our estimate when it sends none), the other figure on one labelled
    /// line, Rivian's spec when it is a usable figure, and our estimate's history as dots. Plain figures: no score, no
    /// comparison with the spec, no "degradation".
    @ViewBuilder
    private var packCard: some View {
        ThemeSection(title: "Battery capacity") {
            if model.pack == nil && model.trends == nil {
                ProgressView().frame(maxWidth: .infinity)
            } else if model.pack?.value == nil && model.trends?.value == nil {
                Text("Couldn't load battery capacity.").themeText(.subline, color: Theme.textTertiary)
            } else {
                let b = TruckLogic.battery(pack: model.pack?.value, trends: model.trends?.value)
                VStack(alignment: .leading, spacing: 6) {
                    switch b.head {
                    case let .reported(kwh)?:
                        (Text("\(TruckLogic.fixed(kwh, 1)) kWh ").font(Theme.sans(ThemeTokens.typeTitle, .semibold, relativeTo: .title)).foregroundStyle(Theme.textPrimary)
                            + Text("usable, as your truck reports it").font(Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body)).foregroundStyle(Theme.textSecondary))
                            .monospacedDigit()
                    case let .estimate(kwh)?:
                        (Text("About \(TruckLogic.fixed(kwh, 0)) kWh ").font(Theme.sans(ThemeTokens.typeTitle, .semibold, relativeTo: .title)).foregroundStyle(Theme.textPrimary)
                            + Text("usable (estimate)").font(Theme.sans(ThemeTokens.typeValue, .regular, relativeTo: .body)).foregroundStyle(Theme.textSecondary))
                            .monospacedDigit()
                        Text("from \(b.charges) \(b.charges == 1 ? "charge" : "charges")").themeText(.subline, color: Theme.textTertiary)
                    case nil:
                        EmptyView()
                    }
                    if let line = b.estimateLine { Text(line).themeText(.value, color: Theme.textSecondary) }
                    if b.spec > 0 { Text("Rivian's spec for this pack: \(TruckLogic.fixed(b.spec, 0)) kWh").themeText(.subline, color: Theme.textTertiary) }
                    if let median = b.estimateKwh {
                        CapacityChart(samples: model.trends?.value?.capacity?.samples ?? [], median: median, spec: b.spec, xLabels: stacked ? 2 : 3)
                            .frame(minHeight: chartHeight)
                        Text("Our estimate comes from charges that add at least \(TruckLogic.whole(b.need))% of battery. It moves a little with temperature and is not a Rivian figure. Charging losses and how the truck reports charge level can shift it by a few percent.")
                            .themeText(.subline, color: Theme.textTertiary)
                    }
                    if let p = b.progress { Text(p).themeText(.value, color: Theme.textSecondary) }
                    if b.untracked { Text("Our estimate can't be tracked on this truck yet.").themeText(.value, color: Theme.textSecondary) }
                }
                .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    // MARK: Trends

    private func trendsCard(_ t: TruckTrends) -> some View {
        ThemeSection(title: "Trends for your truck") {
            // Efficiency only: the capacity estimate's history is in Battery capacity.
            VStack(alignment: .leading, spacing: 24) {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Efficiency by month").themeText(.value, color: Theme.textPrimary).fontWeight(.medium)
                    if t.qualifyingMonths >= t.minMonths {
                        let rows = t.efficiency.map { m in (label: TruckLogic.monthLabel(m.month), value: m.distance >= t.minMonthDistance ? (m.efficiency ?? 0) : 0) }
                        Chart(Array(rows.enumerated()), id: \.offset) { _, r in
                            BarMark(x: .value("Month", r.label), y: .value(t.efficiencyUnit, r.value))
                                .foregroundStyle(Theme.chartMilesBar)
                                .cornerRadius(2)
                        }
                        .chartYAxis { AxisMarks(position: .leading) { v in
                            AxisGridLine().foregroundStyle(Theme.border.opacity(0.55))
                            AxisValueLabel { if let d = v.as(Double.self) { TruckAxisText(TruckLogic.fixed(d, 1)) } }
                        } }
                        .chartXAxis { AxisMarks { v in AxisValueLabel { if let s = v.as(String.self) { TruckAxisText(s) } } } }
                        .frame(height: chartHeight)
                        .accessibilityLabel("Efficiency by month, \(t.efficiencyUnit)")
                        Text("\(t.efficiencyUnit), pack-side. Months under \(TruckLogic.whole(t.minMonthDistance)) \(t.distanceUnit) of driving are left blank. It includes weather and driving style, so cold months usually run lower.")
                            .themeText(.subline, color: Theme.textTertiary)
                        if let note = TruckLogic.trailerTrendNote(t) {
                            Text(note).themeText(.subline, color: Theme.textTertiary)
                        }
                    } else {
                        Text("Not enough driving yet for a trend: \(t.qualifyingMonths) of \(t.minMonths) months with at least \(TruckLogic.whole(t.minMonthDistance)) \(t.distanceUnit). This fills in as you drive.")
                            .themeText(.value, color: Theme.textSecondary)
                    }
                }
            }
            .fixedSize(horizontal: false, vertical: true)
        }
    }

    // MARK: Range at different temperatures

    private func coldCard(_ d: ColdRange) -> some View {
        ThemeSection(title: "Range at different temperatures") {
            if d.reason == "not_tracked" {
                Text("Can't be tracked on this truck yet.").themeText(.value, color: Theme.textSecondary)
            } else {
                VStack(alignment: .leading, spacing: 12) {
                    TruckFlow(spacing: 8) {
                        ForEach(TruckLogic.grid(fahrenheit: fahrenheit), id: \.self) { t in
                            let on = TruckLogic.near(t, temp)
                            Button { pickTemp(t) } label: {
                                Text(TruckLogic.chip(t, fahrenheit: fahrenheit))
                                    .themeText(.value, color: on ? Theme.accent : Theme.textPrimary)
                                    .padding(.horizontal, 3 * Theme.unit).padding(.vertical, 1.5 * Theme.unit)
                                    .background(on ? Theme.accentButton.opacity(0.2) : .clear, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                                    .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(on ? Theme.accentFill.opacity(0.6) : Theme.borderStrong, lineWidth: 1))
                            }
                            .buttonStyle(.plain)
                            .disabled(TruckLogic.chipOff(d, t) && !on)
                            .opacity(TruckLogic.chipOff(d, t) && !on ? 0.4 : 1)
                            .accessibilityAddTraits(on ? .isSelected : [])
                        }
                    }
                    coldResult(d)
                }
                .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    @ViewBuilder
    private func coldResult(_ d: ColdRange) -> some View {
        let at = TruckLogic.chip(temp, fahrenheit: fahrenheit)
        if d.reason == "not_enough_data" || d.efficiencyLow == nil || d.efficiencyHigh == nil {
            Text("Not enough drives near \(at) yet. It needs about \(d.minDrives) drives and \(TruckLogic.whole(d.minMiles)) \(d.distanceUnit) near that temperature.")
                .themeText(.value, color: Theme.textSecondary)
        } else if let lo = d.efficiencyLow, let hi = d.efficiencyHigh {
            let eff = TruckLogic.band(lo, hi, digits: 1, unit: d.efficiencyUnit)
            VStack(alignment: .leading, spacing: 4) {
                if let rl = d.rangeLow, let rh = d.rangeHigh {
                    (Text("Around \(at), a full charge usually gives about ") + Text(TruckLogic.band(rl, rh, digits: 0, unit: d.distanceUnit)).fontWeight(.semibold)
                        + Text(", from your own drives (estimate)."))
                        .themeText(.value, color: Theme.textPrimary)
                    Text("That is \(eff).").themeText(.subline)
                } else {
                    (Text("Around \(at), your drives average about ") + Text(eff).fontWeight(.semibold) + Text(" (estimate)."))
                        .themeText(.value, color: Theme.textPrimary)
                }
                if let b = d.basedOn {
                    Text("Based on \(b.drives) of your drives (\(TruckLogic.whole(b.miles)) \(d.distanceUnit)) between \(TruckLogic.temperature(b.tempLoC, fahrenheit: fahrenheit)) and \(TruckLogic.temperature(b.tempHiC, fahrenheit: fahrenheit)). The temperature comes from weather data, not from your truck.")
                        .themeText(.subline, color: Theme.textTertiary)
                }
            }
        }
    }

    // MARK: Where you charge

    @ViewBuilder
    private var placesCard: some View {
        switch model.places {
        case .value(let all)?:
            let places = ChargePlace.shown(all)
            if !places.isEmpty { TruckPlacesCard(places: places) }
        case .failed?:
            failedCard("Where you charge", "Couldn't load your charging places.")
        default:
            EmptyView()
        }
    }

    // MARK: Sleep and idle-awake

    private var sleepCard: some View {
        ThemeSection(title: "Sleep and idle-awake") {
            switch model.sleep {
            case nil:
                ProgressView().frame(maxWidth: .infinity)
            case .failed?, .missing?:
                Text("Couldn't load sleep data. Try again in a moment.").themeText(.subline, color: Theme.textTertiary)
            case .value(let days)?:
                let s = TruckLogic.sleepSummary(days, now: now, calendar: calendar)
                VStack(alignment: .leading, spacing: 8) {
                    if let h = model.sleepHealth?.value {
                        let l = TruckLogic.sleepHealthLines(h)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(l.headline).themeText(.value, color: Theme.textPrimary)
                            if let d = l.detail { Text(d).themeText(.subline, color: Theme.textSecondary) }
                            Text(l.basis).themeText(.subline, color: Theme.textTertiary)
                        }
                        .padding(.bottom, Theme.unit * 2)
                        .accessibilityElement(children: .combine)
                    }
                    if let t = s.trouble {
                        Text("\(vehicle.name.isEmpty ? "Your truck" : vehicle.name) woke up again within a minute \(t.failed) of \(t.sleeps) times in the last two days.")
                            .themeText(.subline, color: Theme.textBody)
                            .padding(.horizontal, 3 * Theme.unit).padding(.vertical, 2 * Theme.unit)
                            .background(Theme.estimateFill.opacity(0.1), in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                            .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.estimateFill.opacity(0.4), lineWidth: 1))
                    }
                    if s.bars.isEmpty {
                        Text("No sleep data yet. This fills in as power-state recording builds up history.").themeText(.subline, color: Theme.textTertiary)
                    } else {
                        IdleChart(bars: s.bars, calendar: calendar, period: month.spokenTitle(calendar: calendar)).frame(height: chartHeight)
                        Text("Time per day awake while parked and not charging\(s.averageDays > 0 ? ", averaging \(TruckLogic.minutes(s.average ?? 0)) a day over the last \(s.averageDays) full days" : ""). Today is still in progress. Driving and charging are excluded.\(model.sleepCapped ? " Showing the last 90 days of this period." : "")")
                            .themeText(.caption, color: Theme.textTertiary)
                    }
                }
                .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    // MARK: Vehicle

    @ViewBuilder
    private var vehicleCard: some View {
        let sw = model.state.flatMap(TruckLogic.software)
        let update = model.state.flatMap { NowUpdate($0) }
        if sw != nil || update != nil {
            ThemeSection(title: "Vehicle") {
                if stacked {
                    VStack(alignment: .leading, spacing: 12) {
                        if let update { stackedPair("Software update") { updateCell(update) } }
                        if let sw {
                            stackedPair("Software") { versionCell(sw.current, which: "current", hasNotes: vehicle.ota?.current?.hasNotes) }
                            if let a = sw.available {
                                stackedPair("Update available") { versionCell(a, which: "available", hasNotes: vehicle.ota?.available?.hasNotes) }
                            }
                        }
                    }
                } else {
                    Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 8) {
                        if let update {
                            GridRow {
                                Text("Software update").themeText(.value, color: Theme.textTertiary)
                                updateCell(update).gridColumnAlignment(.trailing).frame(maxWidth: .infinity, alignment: .trailing)
                            }
                        }
                        if let sw {
                            GridRow {
                                Text("Software").themeText(.value, color: Theme.textTertiary)
                                versionCell(sw.current, which: "current", hasNotes: vehicle.ota?.current?.hasNotes)
                            }
                            if let a = sw.available {
                                GridRow {
                                    Text("Update available").themeText(.value, color: Theme.textTertiary)
                                    versionCell(a, which: "available", hasNotes: vehicle.ota?.available?.hasNotes)
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    /// The update state in plain words; when the frame is more than two minutes old, its age under it so "Installing" isn't read as current.
    private func updateCell(_ u: NowUpdate) -> some View {
        let age = model.state?.at.map { max(0, now.timeIntervalSince($0)) }
        return VStack(alignment: stacked ? .leading : .trailing, spacing: 2) {
            Text(u.line()).themeText(.value, color: Theme.textPrimary)
            if let age, age > 120 { Text("As of \(NowFormat.ago(age))").themeText(.caption, color: Theme.textTertiary) }
        }
        .accessibilityElement(children: .combine)
    }

    // MARK: Tires

    /// The four pressures from the truck's last reading, with its age (always: people act on a pressure). A wheel is marked low in plain
    /// text, no icon, below the fixed floor or when Rivian's own status says so, and then says whose word it is.
    @ViewBuilder
    private var tiresCard: some View {
        if let state = model.state, let wheels = TruckLogic.wheels(state) {
            let age = state.at.map { max(0, now.timeIntervalSince($0)) }
            ThemeSection(title: "Tires") {
                VStack(alignment: .leading, spacing: 12) {
                    let columns = stacked ? [GridItem(.flexible(), alignment: .topLeading)] : [GridItem(.flexible(), alignment: .topLeading), GridItem(.flexible(), alignment: .topLeading)]
                    LazyVGrid(columns: columns, alignment: .leading, spacing: 12) {
                        ForEach(wheels) { w in
                            VStack(alignment: .leading, spacing: 2) {
                                Text(w.label).themeText(.subline, color: Theme.textTertiary)
                                Text(wheelValue(w)).themeText(.value, color: Theme.textPrimary)
                                if let flag = TruckLogic.wheelFlag(w) { Text(flag).themeText(.subline, color: Theme.estimate) }
                            }
                            .accessibilityElement(children: .combine)
                        }
                    }
                    Text("As of \(age.map(NowFormat.ago) ?? "a while ago"). " + TruckLogic.tiresNote)
                        .themeText(.caption, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    private func wheelValue(_ w: TruckLogic.Wheel) -> String {
        guard let bar = w.bar, let psi = w.psi else { return "—" }
        return "\(TruckLogic.fixed(psi, 0)) psi · \(TruckLogic.fixed(bar, 2)) bar"
    }

    private func stackedPair<V: View>(_ label: String, @ViewBuilder value: () -> V) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(label).themeText(.value, color: Theme.textTertiary)
            value()
        }
        .accessibilityElement(children: .combine)
    }

    private func versionCell(_ version: String, which: String, hasNotes: Bool?) -> some View {
        (stacked ? AnyLayout(VStackLayout(alignment: .leading, spacing: 2)) : AnyLayout(HStackLayout(spacing: 8))) {
            Text(version).themeText(.value, color: Theme.textPrimary)
            if hasNotes == true {
                ReleaseNotesLink(vehicleID: vehicle.id, which: which, fetch: notesLink)
            }
        }
        .frame(maxWidth: .infinity, alignment: stacked ? .leading : .trailing)
    }
}

// MARK: - Pieces

/// A card's title over a quiet placeholder while its first read is out, so the slot doesn't look missing.
private struct TruckLoading: View {
    let title: String

    var body: some View {
        ThemeSection(title: title) {
            VStack(alignment: .leading, spacing: 8) {
                RoundedRectangle(cornerRadius: 4).fill(Theme.border.opacity(0.7)).frame(width: 160, height: 32)
                RoundedRectangle(cornerRadius: 4).fill(Theme.border.opacity(0.6)).frame(height: 12)
            }
            .accessibilityLabel("Loading \(title.lowercased())")
        }
    }
}

private struct TruckAxisText: View {
    let text: String
    init(_ text: String) { self.text = text }
    var body: some View { Text(text).font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption2)).foregroundStyle(Theme.textTertiary) }
}

/// The daily drain bars: a measured day is a bar against the series' own maximum (the dashed line), a day the battery
/// rose is faint, and a left-out day keeps its slot as an outline.
private struct DrainBars: View {
    let daily: [DrainDay]

    var body: some View {
        let (bars, top) = TruckLogic.drainBars(daily)
        VStack(alignment: .leading, spacing: 4) {
            GeometryReader { g in
                let slot = g.size.width / CGFloat(max(1, bars.count))
                let h = g.size.height
                ZStack(alignment: .topLeading) {
                    Path { p in p.move(to: CGPoint(x: 0, y: 1)); p.addLine(to: CGPoint(x: g.size.width, y: 1)) }
                        .stroke(Theme.accentFill.opacity(0.25), style: StrokeStyle(lineWidth: 1, dash: [3, 3]))
                    ForEach(Array(bars.enumerated()), id: \.offset) { i, b in
                        let x = CGFloat(i) * slot + slot * 0.1
                        if b.value == nil {
                            RoundedRectangle(cornerRadius: 1).strokeBorder(Theme.accentFill.opacity(0.35), lineWidth: 1)
                                .frame(width: slot * 0.8, height: h * 9 / 40).offset(x: x, y: h * 30 / 40)
                        } else {
                            let bh: CGFloat = max(1, CGFloat(b.height) * h * 38 / 40)
                            RoundedRectangle(cornerRadius: 1).fill(Theme.accentFill.opacity(b.gained != nil ? 0.5 : 1))
                                .frame(width: slot * 0.8, height: bh).offset(x: x, y: h * 39 / 40 - bh)
                        }
                    }
                }
            }
            .frame(height: 64)
            .accessibilityElement()
            .accessibilityLabel("Daily parked drain, last \(bars.count) days. Left-out days are outlined.")
            Text("Daily drain, % per day. Dashed line = \(TruckLogic.pct(top))%. Outlined days are left out.").themeText(.caption, color: Theme.textTertiary)
        }
    }
}

/// The pack-capacity trace: effective capacity over time on a tight scale, and the newest point.
private struct PackSparkline: View {
    let samples: [PackHealth.Sample]

    var body: some View {
        GeometryReader { g in
            let xs = samples.map { $0.at.timeIntervalSince1970 }
            let ys = samples.map(\.packKwhEffective)
            let x0 = xs.min() ?? 0, x1 = xs.max() ?? 1
            let lo = ys.min() ?? 0, hi = ys.max() ?? 1
            let pad = max((hi - lo) * 0.15, 1)
            let y0 = lo - pad, y1 = hi + pad
            let inset: CGFloat = 4
            let sx = { (x: Double) in inset + CGFloat((x - x0) / max(1, x1 - x0)) * (g.size.width - inset * 2) }
            let sy = { (y: Double) in g.size.height - inset - CGFloat((y - y0) / max(0.001, y1 - y0)) * (g.size.height - inset * 2) }
            ZStack(alignment: .topLeading) {
                Path { p in
                    for (i, s) in samples.enumerated() {
                        let pt = CGPoint(x: sx(s.at.timeIntervalSince1970), y: sy(s.packKwhEffective))
                        if i == 0 { p.move(to: pt) } else { p.addLine(to: pt) }
                    }
                }
                .stroke(Theme.home, lineWidth: 1.5)
                if let last = samples.last {
                    Circle().fill(Theme.home).frame(width: 5, height: 5)
                        .position(x: sx(last.at.timeIntervalSince1970), y: sy(last.packKwhEffective))
                }
            }
        }
        .accessibilityElement()
        .accessibilityLabel("Effective pack capacity over time")
    }
}

/// Each charge's capacity estimate as a dot, bigger and stronger the more the charge added; the rolling median where
/// estimates are dense (never across an empty month); the median dashed and the rated pack dotted when it falls in
/// range; on the median ±15 % (CapacityMath, the web's rules). A tap shows that charge's date and estimate.
struct CapacityChart: View {
    let samples: [TruckTrends.Capacity.Sample]
    let median: Double
    let spec: Double
    var xLabels = 3

    @State private var selected: Date?

    var body: some View {
        let dots = CapacityMath.dots(samples, median: median)
        let runs = CapacityMath.trendRuns(samples)
        let domain = CapacityMath.yDomain(median: median)
        let showSpec = CapacityMath.specInRange(spec, median: median)
        let picked = selected.flatMap { CapacityMath.nearest(dots, to: $0) }
        VStack(alignment: .leading, spacing: Theme.unit) {
            Text(picked.map(readout) ?? " ")
                .themeText(.subline, color: Theme.textBody)
                .accessibilityHidden(picked == nil)
            Chart {
                RuleMark(y: .value("Median", median)).foregroundStyle(Theme.textSecondary).lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 3]))
                if showSpec {
                    // Labelled at its end, so the dots read as our estimate, not as a gap to it.
                    RuleMark(y: .value("Rivian spec", spec)).foregroundStyle(Theme.textTertiary).lineStyle(StrokeStyle(lineWidth: 1, dash: [1, 3]))
                        .annotation(position: .top, alignment: .trailing, spacing: 2) {
                            Text("Rivian spec").font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption2)).foregroundStyle(Theme.textTertiary)
                        }
                }
                ForEach(Array(runs.enumerated()), id: \.offset) { i, run in
                    ForEach(Array(run.enumerated()), id: \.offset) { _, p in
                        LineMark(x: .value("Date", p.at), y: .value("kWh", min(domain.upperBound, max(domain.lowerBound, p.kwh))), series: .value("Run", i))
                            // A neutral under the green dots, so "typical" reads apart from them.
                            .foregroundStyle(Theme.textBody.opacity(0.6))
                            .lineStyle(StrokeStyle(lineWidth: 2.5, lineCap: .round, lineJoin: .round))
                            .interpolationMethod(.linear)
                    }
                }
                ForEach(dots) { d in
                    PointMark(x: .value("Date", d.at), y: .value("kWh", d.kwh))
                        .symbol {
                            Circle()
                                .fill(d.outside ? Color.clear : Theme.home.opacity(CapacityMath.opacity(d.strength)))
                                .overlay { if d.outside { Circle().stroke(Theme.home, lineWidth: 1.5) } }
                                .frame(width: 2 * CapacityMath.radius(d.strength), height: 2 * CapacityMath.radius(d.strength))
                        }
                        .accessibilityLabel(d.at.formatted(.dateTime.month(.abbreviated).day().year()))
                        .accessibilityValue(SpokenText.words(readout(d)))
                }
                if let picked {
                    RuleMark(x: .value("Picked", picked.at)).foregroundStyle(Theme.textTertiary).lineStyle(StrokeStyle(lineWidth: 1))
                }
            }
            .chartYScale(domain: domain)
            .chartYAxis { AxisMarks(position: .leading, values: [domain.lowerBound, median.rounded(), domain.upperBound]) { v in
                AxisGridLine().foregroundStyle(Theme.border.opacity(0.55))
                AxisValueLabel { if let d = v.as(Double.self) { TruckAxisText(TruckLogic.fixed(d, 0)) } }
            } }
            // The first and last estimates' dates, anchored inside the plot (as the web), and the middle when there's room.
            .chartXAxis {
                let first = dots.first?.at ?? .now, last = dots.last?.at ?? .now
                let marks = xLabels >= 3 ? [first, first.addingTimeInterval(last.timeIntervalSince(first) / 2), last] : [first, last]
                AxisMarks(values: marks) { v in
                    AxisValueLabel(anchor: v.index == 0 ? .topLeading : v.index == marks.count - 1 ? .topTrailing : .top, collisionResolution: .disabled) {
                        if let d = v.as(Date.self) { TruckAxisText(d.formatted(.dateTime.month(.abbreviated).day())) }
                    }
                }
            }
            .chartXScale(domain: (dots.first?.at ?? .now)...max(dots.last?.at ?? .now, (dots.first?.at ?? .now).addingTimeInterval(86_400)))
            .chartXSelection(value: $selected)
            .accessibilityLabel("Estimated usable capacity over time, kWh: \(dots.count) charge estimates around \(TruckLogic.fixed(median, 0)) kWh")
            CapacityLegend(trend: !runs.isEmpty, median: median, spec: showSpec ? spec : nil)
        }
    }

    /// "Oct 3 · about 141 kWh (estimate) · 62 kWh added".
    private func readout(_ d: CapacityMath.Dot) -> String {
        var s = "\(d.at.formatted(.dateTime.month(.abbreviated).day())) · about \(TruckLogic.fixed(d.rawKwh, 0)) kWh (estimate)"
        if let e = d.energyKwh { s += " · \(TruckLogic.fixed(e, 0)) kWh added" }
        return s
    }
}

/// What the marks mean, in words beside each swatch.
private struct CapacityLegend: View {
    let trend: Bool
    let median: Double
    let spec: Double?
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        // At accessibility sizes one entry per row, each wrapping (in a flow row the first entry was wider than the
        // screen and clipped).
        (typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: Theme.unit)) : AnyLayout(ThemeFlow(spacing: 3 * Theme.unit, rowSpacing: Theme.unit))) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.unit) {
                Circle().fill(Theme.home).frame(width: 2 * Theme.unit, height: 2 * Theme.unit)
                Text("One charge's estimate (bigger: more added)").fixedSize(horizontal: false, vertical: true)
            }
            if trend {
                HStack(alignment: .firstTextBaseline, spacing: Theme.unit) {
                    Capsule().fill(Theme.textBody.opacity(0.6)).frame(width: 4 * Theme.unit, height: 2.5)
                    Text("Typical (rolling median)").fixedSize(horizontal: false, vertical: true)
                }
            }
            Text("- - Median \(TruckLogic.fixed(median, 0)) kWh").fixedSize(horizontal: false, vertical: true)
            if let spec { Text("··· Rivian spec \(TruckLogic.fixed(spec, 0)) kWh").fixedSize(horizontal: false, vertical: true) }
        }
        .font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption2))
        .foregroundStyle(Theme.textTertiary)
        .accessibilityElement(children: .combine)
    }
}

/// Idle-awake time per day (ux's Now chart, moved here as on the web): the top and middle of the scale as hours and
/// minutes, and every tenth day labelled.
private struct IdleChart: View {
    let bars: [DayValue]
    let calendar: Calendar
    let period: String

    var body: some View {
        let top = bars.map(\.value).max() ?? 0
        let spoken = ChartMath.spokenDaily(title: "Idle-awake time per day", period: period, bars: bars, calendar: calendar) { ChartMath.spokenMinutes($0) }
        Chart(bars) { d in
            BarMark(x: .value("Day", d.day, unit: .day), y: .value("Minutes", d.value))
                .foregroundStyle(LinearGradient(colors: [Theme.chartIdleAwakeBar, Theme.chartIdleAwakeBar.opacity(0.35)], startPoint: .top, endPoint: .bottom))
                .cornerRadius(2)
        }
        .chartYAxis {
            AxisMarks(position: .leading, values: top > 0 ? [0, top / 2, top] : [0]) { value in
                AxisGridLine().foregroundStyle(Theme.border.opacity(0.55))
                AxisValueLabel { if let v = value.as(Double.self) { TruckAxisText(TruckLogic.minutesShort(v)) } }
            }
        }
        .chartXAxis {
            AxisMarks(values: bars.map(\.day).filter { calendar.component(.day, from: $0) % 10 == 1 }) { value in
                AxisValueLabel(anchor: .top, collisionResolution: .disabled) {
                    if let d = value.as(Date.self) { TruckAxisText(ChartsSection.dayLabel(d, calendar: calendar)) }
                }
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken.label)
        .accessibilityValue(spoken.value)
    }
}

/// Wrapping rows (the web's flex-wrap), for chips and legends.
private struct TruckFlow: Layout {
    var spacing: CGFloat = 12
    var lineSpacing: CGFloat = 6

    private func rows(_ subviews: Subviews, width: CGFloat) -> [[(Int, CGSize)]] {
        var rows: [[(Int, CGSize)]] = [[]]
        var x: CGFloat = 0
        for (i, v) in subviews.enumerated() {
            let size = v.sizeThatFits(ProposedViewSize(width: width, height: nil))
            if x > 0 && x + size.width > width { rows.append([]); x = 0 }
            rows[rows.count - 1].append((i, size))
            x += size.width + spacing
        }
        return rows
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rs = rows(subviews, width: proposal.width ?? .infinity)
        let h = rs.reduce(0) { $0 + ($1.map(\.1.height).max() ?? 0) } + lineSpacing * CGFloat(max(0, rs.count - 1))
        let w = rs.map { r in r.reduce(0) { $0 + $1.1.width } + spacing * CGFloat(max(0, r.count - 1)) }.max() ?? 0
        return CGSize(width: proposal.width ?? w, height: h)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for r in rows(subviews, width: bounds.width) {
            let h = r.map(\.1.height).max() ?? 0
            var x = bounds.minX
            for (i, size) in r {
                subviews[i].place(at: CGPoint(x: x, y: y + (h - size.height) / 2), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += h + lineSpacing
        }
    }
}

/// "Where you charge": every place the account has charged (all trucks, all time) on our own map, the busiest first
/// in a short list under it. The places come from the server's clustering and stay in memory.
private struct TruckPlacesCard: View {
    let places: [ChargePlace]

    @Environment(AppState.self) private var state
    @Environment(\.colorScheme) private var scheme
    /// How the basemap is doing: the list stays whatever happens; a failed map gives way to a line.
    @State private var health = MapHealth.ok

    var body: some View {
        ThemeSection(title: "Where you charge") {
            VStack(alignment: .leading, spacing: 12) {
                if health != .failed, let style = MapStyle.json(dark: scheme == .dark, server: state.mapServer) {
                    ChargePlacesMapView(styleJSON: style, places: places, onHealth: { health = $0 })
                        .allowsHitTesting(false)
                        .frame(height: 220)
                        .clipShape(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
                        .overlay(alignment: .bottomTrailing) { MapAttribution() }
                        .overlay { if health == .noTiles { MapUnavailableOverlay() } }
                        .accessibilityElement()
                        .accessibilityLabel("Map of the places you charge")
                        .onAppear { MapNetwork.prepare(server: state.mapServer, token: state.mapToken) }
                } else {
                    MapMessage(text: "Map unavailable.")
                }
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(places.prefix(6)) { p in
                        HStack(alignment: .firstTextBaseline, spacing: 8) {
                            Circle().fill(ChargePlacesMapView.color(p.key)).frame(width: 8, height: 8).accessibilityHidden(true)
                            Text(p.title).themeText(.value, color: Theme.textPrimary)
                            Spacer(minLength: 8)
                            Text("\(p.sessions) \(TruckLogic.plural(p.sessions, "session", "sessions")) · \(TruckLogic.whole(p.energyKwh)) kWh")
                                .themeText(.subline)
                        }
                        .accessibilityElement(children: .combine)
                    }
                }
                Text("All trucks, all time. Places are where the charges cluster, labelled the same way as Money.")
                    .themeText(.caption, color: Theme.textTertiary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }
}
