import SwiftUI

/// Money: what charging cost this month, from the server's ledger (the web's Money page). Read-only: prices and rates
/// are changed on the web, so those actions open it in the browser. Nothing is stored but the one-time notice.
struct MoneyView: View {
    @Environment(AppState.self) private var state

    @AppStorage("rivolt.selected_vehicle") private var selectedID = ""
    @AppStorage(MoneyModel.observedNoticeKey) private var noticeHidden = false
    @State private var vehicles: [Vehicle] = []
    @State private var vehiclesLoaded = false
    @State private var vehiclesError: String?
    @State private var model = MoneyModel()
    @State private var period = PeriodMonth(containing: .now, calendar: .current)
    @State private var filter: MoneyLogic.Filter = .all
    @State private var metric: MoneyContent.Metric = .cost
    @State private var savingsWhich: MoneyModel.Which = .last
    @State private var showShare = false
    @State private var exportFile: MoneyExportFile?
    @State private var csvBusy = false
    @State private var csvError: String?

    private var selected: Vehicle? { vehicles.first { $0.id == selectedID } ?? vehicles.first }
    /// Only an account with more than one truck sends it; otherwise the ledger covers the account.
    private var scope: String? { vehicles.count > 1 ? selected?.id : nil }

    var body: some View {
        NavigationStack {
            ScrollView {
                Group {
                    if let vehiclesError, model.month == nil {
                        MoneyMessage(icon: "exclamationmark.triangle", text: vehiclesError)
                    } else {
                        VStack(spacing: 16) {
                            YearCardView()
                            OffpeakPromptView()
                            MoneyContent(model: model, filter: $filter, metric: $metric, savingsWhich: $savingsWhich,
                                         noticeHidden: $noticeHidden, serverURL: state.serverURL,
                                         export: MoneyContent.Export(busy: csvBusy, error: csvError,
                                                                     share: { showShare = true }, csv: { Task { await exportCSV() } }))
                        }
                    }
                }
                .padding(.horizontal, 4 * Theme.unit)
                .padding(.vertical, 4 * Theme.unit)
            }
            .background(ThemePageBackground().ignoresSafeArea())
            .toolbar(.hidden, for: .navigationBar)
            .refreshable { await reload() }
            .task { await loadVehicles() }
            .task(id: "\(vehiclesLoaded)|\(scope ?? "")|\(period.start.timeIntervalSince1970)") {
                guard vehiclesLoaded else { return }
                await model.load(source: state.client, vehicleID: scope, period: period)
            }
            .task(id: savingsWhich) { await model.loadSavings(source: state.client, which: savingsWhich) }
            .onChange(of: filter) {
                if filter == .confirm { Task { await model.loadConfirm(source: state.client) } }
            }
            .sheet(isPresented: $showShare) {
                if let m = model.month {
                    MoneyShareSheet(month: m) { await model.offpeakShare(source: state.client, month: m.period) }
                        .followsAppearance()
                }
            }
            // The CSV goes straight to the system share sheet; its temporary file goes when the sheet closes.
            .sheet(item: $exportFile, onDismiss: { exportFile?.remove() }) { file in
                MoneyActivitySheet(file: file) { file.remove(); exportFile = nil }
                    .presentationDetents([.medium, .large])
                    .followsAppearance()
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
        ThemeTitleRow(title: "Money") {
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
            ThemeStepper(title: period.title(calendar: .current), spokenTitle: period.spokenTitle(calendar: .current), canGoForward: !period.isCurrent(now: .now, calendar: .current),
                         previous: { period = period.shifted(by: -1, calendar: .current) },
                         next: { period = period.shifted(by: 1, calendar: .current) })
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

    /// The month as the web's CSV, cut in the same zone and for the same truck as the page. Read only when asked.
    private func exportCSV() async {
        guard let m = model.month, !csvBusy else { return }
        csvBusy = true
        csvError = nil
        defer { csvBusy = false }
        do {
            let data = try await state.client.ledgerCSV(month: m.period, timeZone: TimeZone.current.identifier, vehicleID: scope)
            exportFile = try MoneyExportFile.csv(data, month: m.period)
        } catch {
            csvError = "Couldn't make the CSV. \(UserFacingError.words(error))"
        }
    }

    private func reload() async {
        await loadVehicles()
        async let m: Void = model.load(source: state.client, vehicleID: scope, period: period, force: true)
        async let s: Void = model.loadSavings(source: state.client, which: savingsWhich, force: true)
        _ = await (m, s)
        if filter == .confirm { await model.loadConfirm(source: state.client, force: true) }
    }
}

/// A centred line for a screen with nothing else to show.
private struct MoneyMessage: View {
    let icon: String
    let text: String

    var body: some View {
        VStack(spacing: 8) {
            Image(systemName: icon).font(.title)
            Text(text).themeText(.value, color: Theme.textSecondary).multilineTextAlignment(.center)
        }
        .foregroundStyle(Theme.textSecondary)
        .frame(maxWidth: .infinity)
        .padding(.top, 80)
    }
}

/// The page under the title row, drawn from the model alone (so tests can render it from the fixtures): the notice, the
/// statement, the energy split, the off-peak estimate, the receipts and "About these numbers", in the web's order.
struct MoneyContent: View {
    enum Metric: String { case cost, kwh }

    let model: MoneyModel
    @Binding var filter: MoneyLogic.Filter
    @Binding var metric: Metric
    @Binding var savingsWhich: MoneyModel.Which
    @Binding var noticeHidden: Bool
    var serverURL: URL?
    var now: Date = .now
    var calendar: Calendar = .current
    var usesMetric: Bool = NowFormat.usesMetric
    /// Share statement and Download CSV; nil draws them disabled (tests).
    var export: Export?

    struct Export {
        var busy = false
        var error: String?
        let share: () -> Void
        let csv: () -> Void
    }
    @Environment(\.openURL) private var openURL
    @Environment(\.dynamicTypeSize) private var typeSize
    @Environment(\.isDemo) private var isDemo
    @AppStorage(ShareNudgeLogic.dismissedKey) private var shareNudgeGone = false

    var body: some View {
        VStack(spacing: 16) {
            if let m = model.month, let st = model.statement(now: now, calendar: calendar) {
                if st.showObservedNotice && !noticeHidden { observedNotice }
                hero(st)
                if st.empty {
                    emptyCard(m)
                } else {
                    splitCard(m)
                    if MoneyLogic.showsSavingsCard(model.savings, failed: model.savingsFailed) { savingsCard(currency: m.currency) }
                    receiptsCard(m, toConfirm: st.toConfirm)
                }
                if !st.notes.isEmpty {
                    ThemeSection(title: "About these numbers") {
                        VStack(alignment: .leading, spacing: 4) {
                            ForEach(st.notes, id: \.self) { n in
                                HStack(alignment: .firstTextBaseline, spacing: 8) {
                                    Text("•").themeText(.subline)
                                    Text(n).themeText(.subline).fixedSize(horizontal: false, vertical: true)
                                }
                            }
                        }
                    }
                }
            } else if model.unavailable {
                MoneyMessage(icon: "creditcard", text: "Money isn't available for this account on this server yet.")
            } else if let e = model.error {
                MoneyMessage(icon: "exclamationmark.triangle", text: "Couldn't load this month. \(e)")
            } else {
                ProgressView().controlSize(.large).padding(.top, 80)
            }
        }
    }

    private func url(_ link: MoneyLink) -> URL? {
        guard let serverURL else { return nil }
        return URL(string: link.path, relativeTo: serverURL)?.absoluteURL
    }

    /// Text with a web page link inside it, as the web words it ("… is set. Set your home rate."). The link opens the
    /// browser; without a server it reads as plain text.
    private func inline(_ before: String, _ label: String, _ to: MoneyLink, _ after: String = "", style: ThemeText = .value,
                        color: Color? = nil) -> some View {
        var text = AttributedString(before)
        var link = AttributedString(label)
        if let u = url(to) {
            link.link = u
            link.underlineStyle = .single
        }
        text += link
        text += AttributedString(after)
        return Text(text).themeText(style, color: color).tint(Theme.accent).fixedSize(horizontal: false, vertical: true)
    }

    // MARK: Notice

    private var observedNotice: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Fast-charging sessions without your own rate are now estimated from a typical price (DCFC Tracker), not an old table. Add the rate you pay for exact costs.")
                .themeText(.value).fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 16) {
                if let u = url(.networkRates) {
                    Link(destination: u) { Text("Set your rate").themeText(.subline, color: Theme.estimate).fontWeight(.medium) }
                }
                Button { noticeHidden = true } label: { Text("Got it").themeText(.subline) }.buttonStyle(.plain)
            }
        }
        .themeCard()
        .accessibilityElement(children: .contain)
    }

    // MARK: Statement

    /// The one-time card under the statement's buttons; its button opens the same masked share sheet as the header's.
    private var shareNudge: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(ShareNudgeLogic.title).themeText(.value, color: Theme.textPrimary).accessibilityAddTraits(.isHeader)
            Text(ShareNudgeLogic.body).themeText(.subline, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 12) {
                Button(ShareNudgeLogic.shareButton) { shareNudgeGone = true; export?.share() }.buttonStyle(ThemeButtonStyle())
                Button(ShareNudgeLogic.laterButton) { shareNudgeGone = true }.buttonStyle(ThemeSecondaryButtonStyle())
            }
        }
        .padding(3 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .padding(.vertical, 4)
        .accessibilityElement(children: .contain)
    }

    private func hero(_ st: MoneyStatement) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            if st.isCurrentMonth || st.partialFrom != nil {
                MoneyFlow(spacing: 8) {
                    if st.isCurrentMonth { ThemePill(text: "Month to date") }
                    if let from = st.partialFrom { ThemePill(text: "Partial month · data from \(MoneyLogic.shortDay(from, calendar: calendar))") }
                }
                .padding(.bottom, 4)
            }
            MoneyFlow(spacing: 16, lineSpacing: 4, alignment: .lastTextBaseline) {
                Text(MoneyLogic.money(st.headline, st.currency)).themeText(.hero).accessibilityLabel("Spent on charging: \(MoneyLogic.money(st.headline, st.currency))")
                if let d = st.delta {
                    Text("\(d > 0 ? "▲" : "▼") \(Int(abs(d * 100).rounded()))% vs \(st.deltaLabel)")
                        .themeText(.value, color: d > 0 ? Theme.estimate : Theme.accent)
                }
            }
            exportButtons(st)
            if ShareNudgeLogic.shows(empty: st.empty, canShare: export != nil, demo: isDemo, dismissed: shareNudgeGone) { shareNudge }
            Text(st.subline).themeText(.value, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
            if let n = st.energyNote { Text(n).themeText(.subline, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true) }

            if let z = st.zero {
                zeroNotice(z)
            }
            if let n = st.toConfirm, n > 0 {
                Button { filter = .confirm } label: {
                    Text("\(n) \(MoneyLogic.plural(n, "charge", "charges")) to confirm →")
                        .themeText(.subline, color: Theme.estimate).fontWeight(.medium)
                        .padding(.horizontal, 2.5 * Theme.unit).padding(.vertical, Theme.unit)
                        .background(Theme.estimateFill.opacity(0.1), in: Capsule())
                        .overlay(Capsule().strokeBorder(Theme.estimateFill.opacity(0.4), lineWidth: 1))
                }
                .buttonStyle(.plain)
                .padding(.top, 4)
            }
            if let b = st.billed {
                MoneyFlow(spacing: 12, lineSpacing: 2) {
                    (Text("\(MoneyLogic.money(b.billed, st.currency)) billed by Rivian").foregroundStyle(Theme.accent)
                        + Text(b.billedSessions.map { " · \($0) \(MoneyLogic.plural($0, "session", "sessions"))" } ?? "").foregroundStyle(Theme.textTertiary))
                        .themeText(.subline, color: nil)
                    (Text("\(MoneyLogic.money(b.estimated, st.currency)) estimated").foregroundStyle(Theme.textSecondary)
                        + Text(b.estimatedSessions.map { " · \($0) \(MoneyLogic.plural($0, "session", "sessions"))" } ?? "").foregroundStyle(Theme.textTertiary))
                        .themeText(.subline, color: nil)
                }
            }
            if let o = st.observed {
                Text("Includes \(MoneyLogic.money(o.cost, st.currency)) estimated from typical prices (\(o.sessions) \(MoneyLogic.plural(o.sessions, "session", "sessions")))")
                    .themeText(.subline)
            }
            if let f = st.unpricedFast {
                Text("\(f) fast-charging \(MoneyLogic.plural(f, "session", "sessions")) not priced yet").themeText(.subline, color: Theme.estimate)
            }
            if let pct = st.estimatedPercent {
                (Text("Includes ") + Text("\(pct)% estimated").underline(pattern: .dot) + Text(" from charging-network rates."))
                    .themeText(.subline, color: Theme.textTertiary)
            }
            if !st.issues.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(Array(st.issues.enumerated()), id: \.offset) { _, issue in issueRow(issue) }
                }
                .padding(.top, 8)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .themeCard()
    }

    /// The web's two small buttons under the headline. Nothing to share or download for an empty month.
    private func exportButtons(_ st: MoneyStatement) -> some View {
        let off = st.empty || export == nil
        // At an accessibility size the two buttons stack rather than share a narrow row.
        let row = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 8)) : AnyLayout(HStackLayout(spacing: 8))
        return VStack(alignment: .leading, spacing: 4) {
            row {
                Button("Share statement") { shareNudgeGone = true; export?.share() }
                    .accessibilityHint("An image of your month. No places or addresses; cost only if you add it.")
                // The CSV is a read of the signed-in account's own file; the demo has none.
                if !isDemo {
                    Button { export?.csv() } label: {
                        HStack(spacing: 6) {
                            Text("Download CSV")
                            if export?.busy == true { ProgressView().controlSize(.mini) }
                        }
                    }
                    .accessibilityHint("This month's totals and sessions as a spreadsheet file.")
                    .disabled(export?.busy == true)
                }
            }
            .buttonStyle(MoneySmallButtonStyle())
            .disabled(off)
            if let e = export?.error {
                Text(e).themeText(.subline, color: Theme.critical).fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(.vertical, 4)
    }

    private func zeroNotice(_ z: (sessions: Int, kwh: Double, noRate: Bool)) -> some View {
        let lead = "$0 doesn't mean charging was free. \(z.sessions) \(MoneyLogic.plural(z.sessions, "session", "sessions")) (\(MoneyLogic.fixed(z.kwh, 1)) kWh) can't be priced yet"
        return Group {
            if z.noRate {
                inline(lead + " because no home rate is set. ", "Set your home rate", .homeRate, ".", color: Theme.textBody)
            } else {
                Text(lead + ".").themeText(.value, color: Theme.textBody).fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(.horizontal, 3 * Theme.unit).padding(.vertical, 2 * Theme.unit)
        .frame(maxWidth: .infinity, alignment: .leading)
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .padding(.top, 8)
    }

    @ViewBuilder
    private func issueRow(_ issue: MoneyStatement.Issue) -> some View {
        if issue.amber {
            ((issue.lead.map { Text($0).fontWeight(.medium).foregroundStyle(Theme.estimate) } ?? Text("")) + Text(issue.text).foregroundStyle(Theme.textBody))
                .themeText(.subline, color: nil)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.horizontal, 3 * Theme.unit).padding(.vertical, 2 * Theme.unit)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Theme.estimateFill.opacity(0.1), in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.estimateFill.opacity(0.3), lineWidth: 1))
        } else {
            if let l = issue.link {
                inline(issue.text, l.label, l.to, l.after, style: .subline)
            } else {
                Text(issue.text).themeText(.subline).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private func emptyCard(_ m: LedgerMonth) -> some View {
        let rateSet = model.homeRateSet == true
        return VStack(alignment: .leading, spacing: 8) {
            Text(rateSet ? "No charges recorded yet" : "Nothing priced yet").themeText(.value, color: Theme.textPrimary).fontWeight(.semibold)
            Text(rateSet
                 ? "No charges recorded in \(MoneyLogic.monthTitle(m.period, calendar: calendar)). They appear here as you charge."
                 : "Charges show up here with their cost once Rivolt has recorded one. Set your home rate in Settings → Charging so home charges are priced as they arrive.")
                .themeText(.value, color: Theme.textSecondary).fixedSize(horizontal: false, vertical: true)
            if !rateSet, let u = url(.homeRate) {
                Button("Set home rate") { openURL(u) }.buttonStyle(ThemeSecondaryButtonStyle()).padding(.top, 4)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .themeCard()
    }

    // MARK: Split

    private func splitCard(_ m: LedgerMonth) -> some View {
        ActionSection(title: "Where the energy came from") {
            SmallSwitch(options: [(Metric.cost, "$"), (.kwh, "kWh")], selection: $metric, label: "Split by")
        } content: {
            VStack(alignment: .leading, spacing: 16) {
                ForEach(MoneyLogic.splitBars(m)) { bar in MoneySplitBar(bar: bar, currency: m.currency, metric: metric) }
            }
        }
    }

    // MARK: Off-peak savings

    private func savingsCard(currency fallback: String) -> some View {
        let ym = MoneyModel.savingsPeriod(savingsWhich, now: now, calendar: calendar)
        let monthTitle = MoneyLogic.monthTitle(ym, calendar: calendar)
        let currency = model.savings?.currency ?? fallback
        return ActionSection(title: "Off-peak savings") {
            SmallSwitch(options: [(MoneyModel.Which.last, "Last month"), (.now, "This month so far")], selection: $savingsWhich, label: "Month")
        } content: {
            VStack(alignment: .leading, spacing: 8) {
                Text(monthTitle).themeText(.subline, color: Theme.textTertiary)
                if let s = model.savings {
                    switch MoneyLogic.savingsView(s) {
                    case .flat:
                        EmptyView() // the card is not shown for a flat rate (MoneyLogic.showsSavingsCard)
                    case .needs(.schedule):
                        needs("Add your off-peak hours and rate to see what moving home charging would have saved.", "Set off-peak hours", .offpeak)
                        inline("", "My rate is flat, no off-peak hours", .offpeak, style: .subline)
                    case .needs(.homeRate):
                        needs("Set your home rate first, so hours outside your off-peak windows are priced.", "Set home rate", .homeRate)
                    case .needs(.sessions):
                        Text("A few more home charges are needed before this can be estimated for \(monthTitle).").themeText(.value).fixedSize(horizontal: false, vertical: true)
                    case .nothing(let leftOut, let reasons):
                        Text("Nothing to move in \(monthTitle): your home charging already ran at the lowest rate, or no session fit a cheaper window.")
                            .themeText(.value).fixedSize(horizontal: false, vertical: true)
                        if leftOut > 0 { leftOutText(leftOut, reasons) }
                    case let .saved(saved, moved, leftOut, asCharged, ifMoved, reasons, considered, basis):
                        Text(MoneyLogic.savedHeadline(moved: moved, money: MoneyLogic.money(saved, currency)))
                            .themeText(.value, color: Theme.textPrimary).fixedSize(horizontal: false, vertical: true)
                        if let a = asCharged, let i = ifMoved {
                            Text("Home charging as charged \(MoneyLogic.money(a, currency)) · if moved \(MoneyLogic.money(i, currency)) (estimate)").themeText(.subline)
                        }
                        if let c = considered {
                            Text("Based on \(c) home \(MoneyLogic.plural(c, "charge", "charges")).").themeText(.subline, color: Theme.textTertiary)
                        }
                        if !basis.isEmpty { Text(basis).themeText(.subline, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true) }
                        if leftOut > 0 { leftOutText(leftOut, reasons) }
                    }
                } else {
                    Text("…").themeText(.value, color: Theme.textTertiary)
                }
                Text("Estimated from your own home charges and your off-peak hours. Advice only: Rivolt never changes charging on the truck.")
                    .themeText(.caption, color: Theme.textTertiary).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private func needs(_ text: String, _ link: String, _ to: MoneyLink) -> some View {
        inline(text + " ", link, to)
    }

    private func leftOutText(_ n: Int, _ reasons: [String]) -> some View {
        Text(MoneyLogic.leftOutLine(n) + (reasons.isEmpty ? "" : " Why: \(reasons.joined(separator: "; ")).")).themeText(.subline, color: Theme.textTertiary)
            .fixedSize(horizontal: false, vertical: true)
    }

    // MARK: Receipts

    private func receiptsCard(_ m: LedgerMonth, toConfirm: Int?) -> some View {
        let shown = (m.receipts ?? []).filter { filter.matches($0) }
        return ThemeSection(title: "Receipts") {
            MoneyFlow(spacing: 4, lineSpacing: 4) {
                ForEach(MoneyLogic.Filter.shown(current: filter, toConfirm: toConfirm)) { f in
                    MoneyChip(text: f.label, selected: filter == f) { filter = f }
                }
            }
            .padding(.bottom, 4)
            if filter == .confirm {
                confirmList
            } else if shown.isEmpty {
                Text("No receipts match this filter.").themeText(.value, color: Theme.textTertiary)
            } else {
                VStack(spacing: 0) {
                    ForEach(Array(shown.enumerated()), id: \.element.id) { i, r in
                        if i > 0 { Rectangle().fill(Theme.border.opacity(0.7)).frame(height: 1) }
                        MoneyReceiptRow(receipt: r, currency: m.currency, calendar: calendar, usesMetric: usesMetric, url: url)
                    }
                }
            }
            (Text("One receipt per plug-in; top-ups and fragments are folded in. ") + Text("Dotted").underline(pattern: .dot)
                + Text(" = estimate · ") + Text("!").foregroundStyle(Theme.estimate) + Text(" = home rate used away from home · Pending = not in the total yet."))
                .themeText(.caption, color: Theme.textTertiary)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.top, 4)
        }
    }

    @ViewBuilder
    private var confirmList: some View {
        if let rows = model.confirm {
            if rows.isEmpty {
                Text("Nothing to confirm.").themeText(.value, color: Theme.textTertiary)
            } else {
                let groups = Dictionary(grouping: rows) { MoneyLogic.period(PeriodMonth(containing: $0.startedAt, calendar: calendar), calendar: calendar) }
                VStack(alignment: .leading, spacing: 16) {
                    ForEach(groups.keys.sorted(by: >), id: \.self) { key in
                        VStack(alignment: .leading, spacing: 4) {
                            Text(MoneyLogic.monthTitle(key, calendar: calendar)).themeText(.label).accessibilityAddTraits(.isHeader)
                            ForEach(groups[key] ?? []) { c in confirmRow(c) }
                        }
                    }
                }
            }
        } else {
            ProgressView().frame(maxWidth: .infinity)
        }
    }

    @ViewBuilder
    private func confirmRow(_ c: ConfirmCharge) -> some View {
        let strong = c.networkState == "needs_confirmation"
        let net = c.networkEvidence?.candidates?.first?.network
        let priced = c.pricePerKwh > 0
        let cur = c.currency.flatMap { $0.isEmpty ? nil : $0 } ?? "USD"
        let row = HStack(alignment: .firstTextBaseline, spacing: 12) {
            VStack(alignment: .leading, spacing: 2) {
                Text(MoneyLogic.dateTime(c.startedAt, calendar: calendar)).themeText(.value, color: Theme.textPrimary)
                Text(priced ? "\(net ?? "Network") · \(MoneyLogic.money(c.pricePerKwh, cur, digits: 3))/kWh" : "Pick a network").themeText(.subline)
            }
            Spacer(minLength: 8)
            VStack(alignment: .trailing, spacing: 2) {
                Text(priced ? MoneyLogic.money(c.cost, cur) : "Unpriced").themeText(.value, color: Theme.textPrimary)
                Text(strong ? "Please confirm →" : "Estimated · confirm →").themeText(.caption, color: strong ? Theme.estimate : Theme.textTertiary)
            }
        }
        .padding(.vertical, 6)
        if let u = url(.charge(c.id, pricing: true)) {
            Link(destination: u) { row }.buttonStyle(.plain)
        } else {
            row
        }
    }
}

// MARK: - Pieces

/// A titled card like `ThemeSection`, with a control at the end of the header (the web's `Card` actions). Money and Truck.
struct ActionSection<Actions: View, Content: View>: View {
    let title: String
    @ViewBuilder var actions: Actions
    @ViewBuilder var content: Content

    private var heading: some View {
        Text(title)
            .font(Theme.sans(ThemeTokens.typeValue, .medium, relativeTo: .body))
            .foregroundStyle(Theme.textBody)
            .accessibilityAddTraits(.isHeader)
            .fixedSize(horizontal: false, vertical: true)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // One line when the title and the control fit together; the control drops under the title when they don't.
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 8) {
                    heading
                    Spacer(minLength: 8)
                    actions.fixedSize()
                }
                VStack(alignment: .leading, spacing: 8) {
                    heading
                    actions.fixedSize()
                }
            }
            .padding(.horizontal, 4 * Theme.unit).padding(.vertical, 2 * Theme.unit)
            Rectangle().fill(Theme.border).frame(height: 1)
            content
                .padding(4 * Theme.unit)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .themeCard(padding: 0)
    }
}

/// The web's small bordered button (Share statement, Download CSV).
private struct MoneySmallButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View { Look(configuration: configuration) }

    private struct Look: View {
        let configuration: ButtonStyleConfiguration
        @Environment(\.isEnabled) private var isEnabled

        var body: some View {
            configuration.label
                .themeText(.subline, color: Theme.textBody)
                .padding(.horizontal, 2.5 * Theme.unit).padding(.vertical, Theme.unit)
                .background(configuration.isPressed ? Theme.border : .clear, in: RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.borderStrong, lineWidth: 1))
                .opacity(isEnabled ? 1 : 0.4)
        }
    }
}

/// The web's small two-way switch in a card header ("$ | kWh", "Last month | This month so far", "7 days | 30 days").
struct SmallSwitch<T: Hashable>: View {
    let options: [(T, String)]
    @Binding var selection: T
    let label: String

    var body: some View {
        HStack(spacing: 0) {
            ForEach(options.indices, id: \.self) { i in
                let (value, text) = options[i]
                let on = selection == value
                Button { selection = value } label: {
                    Text(text).themeText(.subline, color: on ? Theme.textPrimary : Theme.textSecondary)
                        .lineLimit(1)
                        .padding(.horizontal, 1.5 * Theme.unit).padding(.vertical, Theme.unit / 2)
                        .background(on ? Theme.border : .clear, in: RoundedRectangle(cornerRadius: Theme.radiusInner / 2, style: .continuous))
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(on ? .isSelected : [])
            }
        }
        .padding(2)
        .overlay(RoundedRectangle(cornerRadius: Theme.radiusInner, style: .continuous).strokeBorder(Theme.border, lineWidth: 1))
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .accessibilityElement(children: .contain)
        .accessibilityLabel(label)
    }
}

/// A filter chip: the accent when picked, a hairline otherwise.
private struct MoneyChip: View {
    let text: String
    let selected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(text).themeText(.subline, color: selected ? Theme.accent : Theme.textSecondary)
                .padding(.horizontal, 2 * Theme.unit).padding(.vertical, Theme.unit / 2)
                .background(selected ? Theme.accentFill.opacity(0.15) : .clear, in: Capsule())
                .overlay(Capsule().strokeBorder(selected ? Theme.accentFill.opacity(0.6) : Theme.border, lineWidth: 1))
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// The web's place colours (components/money.tsx): the bar fill, the dot, and the chip's text and border.
private enum PlaceTone {
    static func fill(_ key: MoneyLogic.SplitKey) -> Color {
        switch key {
        case .home, .ac: return Theme.accentFill
        case .away, .publicAC: return Theme.awayFill
        case .dcfc: return Theme.fastFill
        case .unknown: return Theme.textTertiary
        }
    }

    static func dot(_ key: MoneyLogic.SplitKey) -> Color {
        switch key {
        case .home, .ac: return Theme.home
        case .away, .publicAC: return Theme.away
        case .dcfc: return Theme.fast
        case .unknown: return Theme.textSecondary
        }
    }

    static func ink(_ key: MoneyLogic.SplitKey) -> Color {
        switch key {
        case .home, .ac: return Theme.accent
        case .away, .publicAC: return Theme.drive
        case .dcfc: return Theme.estimate
        case .unknown: return Theme.textBody
        }
    }

    static func edge(_ key: MoneyLogic.SplitKey) -> Color {
        key == .unknown ? Theme.borderStrong : fill(key).opacity(0.4)
    }
}

/// One stacked bar, in dollars or kWh (home is most of the energy but little of the money), and its legend.
private struct MoneySplitBar: View {
    let bar: MoneyLogic.SplitBar
    let currency: String
    let metric: MoneyContent.Metric
    @Environment(\.dynamicTypeSize) private var typeSize

    private func amount(_ t: LedgerTotals) -> Double { metric == .cost ? t.cost : t.kwh }
    private func text(_ t: LedgerTotals) -> String { metric == .cost ? MoneyLogic.money(t.cost, currency) : "\(MoneyLogic.fixed(t.kwh, 0)) kWh" }

    var body: some View {
        let total = bar.segments.reduce(0) { $0 + amount($1.totals) }
        VStack(alignment: .leading, spacing: 6) {
            Text(bar.label).themeText(.label)
            GeometryReader { g in
                HStack(spacing: 0) {
                    if total > 0 {
                        ForEach(bar.segments) { s in
                            if amount(s.totals) > 0 { Rectangle().fill(PlaceTone.fill(s.key)).frame(width: g.size.width * amount(s.totals) / total) }
                        }
                    }
                    Spacer(minLength: 0)
                }
            }
            .frame(height: 12)
            .background(Theme.border)
            .clipShape(Capsule())
            .accessibilityElement()
            .accessibilityLabel("\(bar.label): \(bar.segments.map { "\($0.label) \(text($0.totals))" }.joined(separator: ", "))")
            let cols = typeSize.isAccessibilitySize ? 1 : bar.segments.count == 4 ? 2 : bar.segments.count
            let rows = stride(from: 0, to: bar.segments.count, by: cols).map { Array(bar.segments[$0..<min($0 + cols, bar.segments.count)]) }
            Grid(alignment: .topLeading, horizontalSpacing: 8, verticalSpacing: 8) {
                ForEach(rows.indices, id: \.self) { i in
                    GridRow {
                        ForEach(rows[i]) { s in legend(s).frame(maxWidth: .infinity, alignment: .leading) }
                    }
                }
            }
            .padding(.top, 2)
        }
    }

    private func legend(_ s: MoneyLogic.Segment) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Circle().fill(PlaceTone.dot(s.key)).frame(width: 8, height: 8)
                Text(s.label).themeText(.subline)
            }
            Text(text(s.totals)).themeText(.subline, color: Theme.textPrimary)
            Text("\(s.totals.sessions) \(MoneyLogic.plural(s.totals.sessions, "session", "sessions"))").themeText(.subline, color: Theme.textTertiary)
        }
        .accessibilityElement(children: .combine)
    }
}

/// One plug-in: when, the place chip and the figure, then the energy and charge levels, and where the price came from
/// with its one action. Top-ups and fragments fold in and open on request.
private struct MoneyReceiptRow: View {
    let receipt: LedgerReceipt
    let currency: String
    let calendar: Calendar
    let usesMetric: Bool
    let url: (MoneyLink) -> URL?
    @State private var open = false
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let r = receipt
        let line = MoneyReceiptLine(r, currency: currency)
        let key = line.chip.key
        let date = Text(MoneyLogic.dateTime(r.startedAt, calendar: calendar)).themeText(.value, color: Theme.textPrimary)
        let chip = Text(line.chip.label).themeText(.caption, color: PlaceTone.ink(key))
            .padding(.horizontal, 1.5 * Theme.unit).padding(.vertical, 1)
            .overlay(Capsule().strokeBorder(PlaceTone.edge(key), lineWidth: 1))
        HStack(alignment: .top, spacing: 12) {
            Circle().fill(PlaceTone.dot(key)).frame(width: 8, height: 8).padding(.top, 6).accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 3) {
                if typeSize.isAccessibilitySize {
                    // The date gets its own line (never cut short); the place and the figure go under it.
                    date.fixedSize(horizontal: false, vertical: true)
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        chip
                        Spacer(minLength: 8)
                        MoneyValueText(value: line.value)
                    }
                } else {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        date.lineLimit(1)
                        chip
                        Spacer(minLength: 8)
                        MoneyValueText(value: line.value)
                    }
                }
                details(r, line).fixedSize(horizontal: false, vertical: true)
                MoneyFlow(spacing: 12, lineSpacing: 2) {
                    priceLabel(line.label)
                    if let a = line.rateAction, let u = url(.networkRates) {
                        Link(destination: u) { Text(a).themeText(.caption, color: Theme.estimate).fontWeight(.medium) }
                    }
                    if let a = line.action, let u = url(a.to) {
                        Link(destination: u) { Text(a.label).themeText(.caption, color: Theme.estimate).fontWeight(.medium) }
                    }
                    if line.extra > 0 {
                        Button { open.toggle() } label: {
                            Text("\(open ? "Hide" : "+") \(line.extra) \(MoneyLogic.plural(line.extra, "top-up/fragment", "top-ups/fragments"))")
                                .themeText(.caption, color: Theme.textTertiary)
                        }
                        .buttonStyle(.plain)
                        .accessibilityAddTraits(open ? .isSelected : [])
                    }
                }
                if open && line.extra > 0 {
                    VStack(alignment: .leading, spacing: 4) {
                        ForEach(r.sessions, id: \.chargeId) { s in
                            HStack(alignment: .firstTextBaseline) {
                                Text("\(MoneyLogic.dateTime(s.startedAt, calendar: calendar)) · \(s.kwh.map { "\(s.energyEstimated == true ? "~" : "")\(MoneyLogic.fixed($0, 1)) kWh" } ?? "— kWh")\(s.kind == "dcfc" ? " · Fast" : "")")
                                    .themeText(.caption)
                                Spacer(minLength: 8)
                                MoneyValueText(value: Self.sessionValue(s, currency: currency), small: true)
                            }
                        }
                    }
                    .padding(.leading, 3 * Theme.unit)
                    .overlay(alignment: .leading) { Rectangle().fill(Theme.border).frame(width: 1) }
                    .padding(.top, 4)
                }
            }
        }
        .padding(.vertical, 10)
    }

    /// "27.5 kWh  59% → 80%  7.6 kW peak  1215 mi from home  · no energy recorded".
    private func details(_ r: LedgerReceipt, _ line: MoneyReceiptLine) -> Text {
        var parts: [Text] = [r.energyEstimated == true
            ? Text("~\(MoneyLogic.fixed(r.kwh, 1)) kWh").underline(pattern: .dot, color: Theme.textTertiary)
            : Text("\(MoneyLogic.fixed(r.kwh, 1)) kWh")]
        if let a = r.socStart, let b = r.socEnd { parts.append(Text("\(Int(a.rounded()))% → \(Int(b.rounded()))%")) }
        if let p = r.peakKw { parts.append(Text("\(MoneyLogic.fixed(p, p < 10 ? 1 : 0)) kW peak")) }
        if r.place != "home", r.billedVendor != nil || r.billedCharger != nil {
            parts.append(Text([r.billedVendor, r.billedCharger].compactMap { $0 }.joined(separator: " · ")).foregroundStyle(Theme.textBody))
        }
        if r.place == "away", let km = r.kmFromHome {
            parts.append(Text("\(MoneyLogic.distance(km: km, metric: usesMetric)) from home").foregroundStyle(Theme.textTertiary))
        }
        for n in line.notes { parts.append(Text("· \(n)").foregroundStyle(Theme.textTertiary)) }
        return parts.dropFirst().reduce(parts[0]) { $0 + Text("  ") + $1 }
            .font(Theme.sans(ThemeTokens.typeSubline, .regular, relativeTo: .footnote)).monospacedDigit()
            .foregroundColor(Theme.textSecondary)
    }

    @ViewBuilder
    private func priceLabel(_ label: MoneyReceiptLine.Label) -> some View {
        switch label {
        case .billed:
            Text("Billed by Rivian").themeText(.caption, color: Theme.accent).fontWeight(.medium)
        case .partly(let amount):
            (Text("Partly billed").fontWeight(.medium).foregroundStyle(Theme.textBody) + Text(" · \(amount) billed by Rivian").foregroundStyle(Theme.textTertiary))
                .themeText(.caption, color: nil)
        case let .plain(prefix, text, amber):
            ((prefix ? Text("Estimated · ").fontWeight(.medium).foregroundStyle(Theme.textBody) : Text(""))
                + Text(text).foregroundStyle(amber ? Theme.estimate : Theme.textTertiary))
                .themeText(.caption, color: nil)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    static func sessionValue(_ s: LedgerSession, currency: String) -> MoneyReceiptLine.Value {
        if let b = s.billedCost { return .billed(MoneyLogic.money(b, s.billedCurrency ?? currency)) }
        switch MoneyLogic.grammar(MoneyLogic.Priceable(s)) {
        case .pending: return .pending
        case .unknown: return .dash
        case .free: return .free
        case .estimated: return s.cost.map { .estimated(MoneyLogic.money($0, currency)) } ?? .dash
        case .suspect: return s.cost.map { .suspect(MoneyLogic.money($0, currency)) } ?? .dash
        case .recorded: return s.cost.map { s.energyEstimated == true ? .estimated(MoneyLogic.money($0, currency)) : .recorded(MoneyLogic.money($0, currency)) } ?? .dash
        }
    }
}

/// A figure in the web's price grammar: solid when billed or entered, dotted with "EST." when estimated, an amber "!"
/// when the home rate was used away from home, "Pending" when not in the total, and a dash when unknown.
private struct MoneyValueText: View {
    let value: MoneyReceiptLine.Value
    var small = false

    var body: some View {
        let style: ThemeText = small ? .caption : .value
        switch value {
        case .billed(let s), .recorded(let s):
            Text(s).themeText(style, color: small ? Theme.textSecondary : Theme.textPrimary).fontWeight(small ? .regular : .medium)
        case .free:
            Text("Free").themeText(style, color: Theme.textPrimary)
        case .pending:
            Text("Pending").themeText(style, color: Theme.textTertiary).accessibilityLabel("Pending, not in the total")
        case .dash:
            Text("—").themeText(style, color: Theme.textTertiary).accessibilityLabel("Price unknown")
        case .estimated(let s):
            (Text(s).fontWeight(small ? .regular : .medium).foregroundStyle(small ? Theme.textSecondary : Theme.textPrimary).underline(pattern: .dot, color: Theme.textTertiary)
                + Text(" EST.").font(Theme.sans(ThemeTokens.typeCaption, .regular, relativeTo: .caption2)).foregroundStyle(Theme.textTertiary))
                .themeText(style, color: nil)
                .accessibilityLabel("\(s), estimate")
        case .suspect(let s):
            (Text(s).fontWeight(small ? .regular : .medium).foregroundStyle(Theme.textPrimary) + Text(" !").foregroundStyle(Theme.estimate))
                .themeText(style, color: nil)
                .accessibilityLabel("\(s), likely wrong")
        }
    }
}

/// Lays its children out in rows, wrapping to the next row when one is full (the web's `flex-wrap`).
private struct MoneyFlow: Layout {
    var spacing: CGFloat = 8
    var lineSpacing: CGFloat = 4
    var alignment: VerticalAlignment = .center

    private func rows(_ subviews: Subviews, width: CGFloat) -> [[(Int, CGSize)]] {
        var rows: [[(Int, CGSize)]] = [[]]
        var x: CGFloat = 0
        for (i, v) in subviews.enumerated() {
            let size = v.sizeThatFits(ProposedViewSize(width: width, height: nil))
            if x > 0 && x + size.width > width {
                rows.append([])
                x = 0
            }
            rows[rows.count - 1].append((i, size))
            x += size.width + spacing
        }
        return rows
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        let rs = rows(subviews, width: width)
        let height = rs.reduce(0) { $0 + ($1.map(\.1.height).max() ?? 0) } + lineSpacing * CGFloat(max(0, rs.count - 1))
        let used = rs.map { r in r.reduce(0) { $0 + $1.1.width } + spacing * CGFloat(max(0, r.count - 1)) }.max() ?? 0
        return CGSize(width: proposal.width ?? used, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for r in rows(subviews, width: bounds.width) {
            let h = r.map(\.1.height).max() ?? 0
            // Baselines line up within a row: each view's baseline at the row's lowest one.
            let base = alignment == .center ? 0 : r.map { subviews[$0.0].dimensions(in: ProposedViewSize($0.1))[alignment] }.max() ?? 0
            var x = bounds.minX
            for (i, size) in r {
                let dy: CGFloat
                if alignment == .center {
                    dy = (h - size.height) / 2
                } else {
                    dy = base - subviews[i].dimensions(in: ProposedViewSize(size))[alignment]
                }
                subviews[i].place(at: CGPoint(x: x, y: y + dy), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += h + lineSpacing
        }
    }
}
