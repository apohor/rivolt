import XCTest
@testable import Rivolt

/// The three charging cards (off-peak, networks, planner defaults): their rules, what is sent and how a save is said.
@MainActor
final class ChargingSettingsTests: XCTestCase {
    // MARK: Off-peak

    private func win(_ days: OffpeakForm.Days = .all, _ s: String, _ e: String, _ rate: String = "0.09", _ label: String = "") -> OffpeakForm.Draft {
        OffpeakForm.Draft(days: days, start: s, end: e, rate: rate, label: label)
    }
    private func win(_ s: String, _ e: String, _ rate: String = "0.09", _ label: String = "") -> OffpeakForm.Draft {
        win(.all, s, e, rate, label)
    }

    func testClockTimesAreTwoDigitsEach() {
        XCTAssertEqual(OffpeakForm.minutes("21:00"), 1260)
        XCTAssertEqual(OffpeakForm.minutes("00:00"), 0)
        XCTAssertNil(OffpeakForm.minutes("24:00"))
        XCTAssertNil(OffpeakForm.minutes("9:00"))
        XCTAssertNil(OffpeakForm.minutes("09:60"))
        XCTAssertEqual(OffpeakForm.fmtClock("21:00"), "9 PM")
        XCTAssertEqual(OffpeakForm.fmtClock("06:30"), "6:30 AM")
        XCTAssertEqual(OffpeakForm.fmtClock("00:00"), "12 AM")
        XCTAssertEqual(OffpeakForm.describe(win(.weekdays, "21:00", "06:00")), "Weekdays, 9 PM to 6 AM")
    }

    func testThePickerCarriesTheWallClockWhateverTheZone() {
        for t in ["00:00", "06:05", "12:00", "21:00", "23:59"] {
            XCTAssertEqual(OffpeakForm.time(OffpeakForm.date(t)), t)
        }
    }

    func testAWindowIsCheckedAsTheServerChecksIt() {
        XCTAssertEqual(OffpeakForm.validate([win("21:00", "06:00")]), [.init()])
        XCTAssertEqual(OffpeakForm.validate([win("21:00", "06:00", "")])[0].rate, "Enter a rate.")
        XCTAssertEqual(OffpeakForm.validate([win("21:00", "06:00", "abc")])[0].rate, "Enter a number.")
        XCTAssertEqual(OffpeakForm.validate([win("21:00", "06:00", "6")])[0].rate, "The rate must be between 0 and 5 per kWh.")
        XCTAssertEqual(OffpeakForm.validate([win("21:00", "06:00", "0.12345")])[0].rate, "Use at most 4 decimal places.")
        XCTAssertEqual(OffpeakForm.validate([win("21:00", "06:00", "0,09")])[0].rate, nil, "a comma keyboard")
        XCTAssertEqual(OffpeakForm.validate([win("07:00", "07:00")])[0].time, "The start and the end can't be the same time.")
        XCTAssertEqual(OffpeakForm.validate([win("", "07:00")])[0].time, "Enter a start and an end time.")
        XCTAssertEqual(OffpeakForm.validate([win("21:00", "06:00", "0.09", String(repeating: "x", count: 41))])[0].label,
                       "The name can be at most 40 characters.")
    }

    func testWindowsThatShareAnHourAreRefusedAcrossMidnightToo() {
        let errs = OffpeakForm.validate([win(.all, "21:00", "06:00"), win(.weekdays, "05:00", "08:00")])
        XCTAssertNil(errs[0].overlap)
        XCTAssertEqual(errs[1].overlap, "Overlaps window 1 (Every day, 9 PM to 6 AM). Change one of them.")
        XCTAssertTrue(OffpeakForm.hasErrors(errs))
        // Friday night runs into Saturday; Saturday morning is a different window and does not clash with Monday 05:00.
        XCTAssertFalse(OffpeakForm.hasErrors(OffpeakForm.validate([win(.weekdays, "21:00", "06:00"), win(.weekends, "09:00", "11:00")])))
    }

    func testTheStripsShowTheHoursOfAWeekdayAndAWeekend() {
        let ws = [win(.weekdays, "21:00", "06:00"), win(.weekends, "00:00", "03:00")]
        let wd = OffpeakForm.coverage(ws, .weekdays)
        XCTAssertEqual(wd.filter { $0 }.count, 6 + 3 /* 00-05 from last night, 21-23 tonight */)
        XCTAssertTrue(wd[22]); XCTAssertTrue(wd[2]); XCTAssertFalse(wd[12])
        let we = OffpeakForm.coverage(ws, .weekends)
        XCTAssertEqual(we.filter { $0 }.count, 6, "Saturday 00:00-06:00 is Friday night running over; the 00:00-03:00 window is inside it")
        XCTAssertEqual(OffpeakForm.spokenCoverage("Weekdays", wd), "Weekdays: 9 off-peak hours of 24")
        XCTAssertEqual(OffpeakForm.spokenCoverage("Weekends", Array(repeating: false, count: 24)), "Weekends: no off-peak hours")
    }

    func testTheWindowsGoOutWithRatesToFourPlacesAndNoEmptyName() {
        let out = OffpeakForm.windows([win(.weekends, "09:00", "11:30", "0,1234", "  "), win(.all, "21:00", "06:00", "0.09", " Overnight ")])
        XCTAssertEqual(out, [OffpeakWindow(days: "weekends", start: "09:00", end: "11:30", rate: 0.1234, label: nil),
                             OffpeakWindow(days: "all", start: "21:00", end: "06:00", rate: 0.09, label: "Overnight")])
    }

    func testSaveIsLiveOnlyAfterAChange() {
        let saved = OffpeakSchedule(tz: "America/Chicago", windows: [OffpeakWindow(days: "all", start: "21:00", end: "06:00", rate: 0.09, label: nil)])
        let rows = OffpeakForm.drafts(saved)
        XCTAssertEqual(rows.count, 1)
        XCTAssertFalse(OffpeakForm.changed(rows, tz: "America/Chicago", saved: saved))
        XCTAssertTrue(OffpeakForm.changed(rows, tz: "America/New_York", saved: saved))
        var edited = rows; edited[0].rate = "0.08"
        XCTAssertTrue(OffpeakForm.changed(edited, tz: "America/Chicago", saved: saved))
        XCTAssertTrue(OffpeakForm.changed([], tz: "America/Chicago", saved: saved), "removing the last window clears the schedule")
    }

    func testTheZonePickerAlwaysListsTheScheduleOwnZone() {
        XCTAssertTrue(OffpeakForm.zones(including: "America/Chicago").contains("America/Chicago"))
        XCTAssertEqual(OffpeakForm.zones(including: "Mars/Olympus").first, "Mars/Olympus")
    }

    func testTheOffpeakBodyIsTheScheduleAndNothingElse() throws {
        let body = OffpeakPut(tz: "UTC", windows: [OffpeakWindow(days: "weekdays", start: "21:00", end: "06:00", rate: 0.09, label: nil)])
        let enc = JSONEncoder(); enc.keyEncodingStrategy = .convertToSnakeCase; enc.outputFormatting = [.sortedKeys]
        XCTAssertEqual(String(data: try enc.encode(body), encoding: .utf8),
                       #"{"tz":"UTC","windows":[{"days":"weekdays","end":"06:00","rate":0.09,"start":"21:00"}]}"#)
        XCTAssertEqual(String(data: try enc.encode(FlatRateBody(flatRate: true)), encoding: .utf8), #"{"flat_rate":true}"#)
    }

    func testTheOffpeakAnswerDecodes() throws {
        let json = #"{"tz":"America/Chicago","windows":[{"days":"all","start":"21:00","end":"06:00","rate":0.09,"label":"Overnight"}],"configured":true,"base_rate":0.15,"currency":"USD","flat_rate":false}"#
        let s = try JSON.decoder().decode(OffpeakSchedule.self, from: Data(json.utf8))
        XCTAssertEqual(s.windows.first?.label, "Overnight")
        XCTAssertEqual(s.baseRate, 0.15)
        XCTAssertEqual(s.flatRate, false)
    }

    // MARK: Networks

    private func net(_ name: String, _ price: Double, member: Double? = nil, slug: String? = nil, preferred: Bool? = nil) -> ChargingNetwork {
        ChargingNetwork(name: name, pricePerKwh: price, currency: "USD", memberPricePerKwh: member, slug: slug, preferred: preferred)
    }

    func testARateIsSetWhenGuestOrMemberIsAboveZero() {
        XCTAssertFalse(ChargingNetworksForm.rateSet(net("A", 0)))
        XCTAssertTrue(ChargingNetworksForm.rateSet(net("A", 0.48)))
        XCTAssertTrue(ChargingNetworksForm.rateSet(net("A", 0, member: 0.36)))
    }

    func testTheMemberSavingNeedsBothPricesAndALowerMemberRate() {
        XCTAssertEqual(ChargingNetworksForm.memberSaving(net("A", 0.48, member: 0.36)), 0.12, accuracy: 1e-9)
        XCTAssertEqual(ChargingNetworksForm.memberSaving(net("A", 0.48)), 0)
        XCTAssertEqual(ChargingNetworksForm.memberSaving(net("A", 0, member: 0.36)), 0)
        XCTAssertEqual(ChargingNetworksForm.memberSaving(net("A", 0.3, member: 0.4)), 0)
    }

    func testPreferredNetworksComeFirstThenByNameAndTheIndexesAreTheRowsOwn() {
        let rows = [net("Zap", 0.3), net("evgo", 0.4, preferred: true), net("Electrify America", 0.5), net("Ionna", 0.5, preferred: true)]
        XCTAssertEqual(ChargingNetworksForm.displayOrder(rows), [1, 3, 2, 0])
    }

    func testALocationIsLatLonInRangeAndNotZeroZero() {
        XCTAssertEqual(ChargingNetworksForm.parseLocation(""), .none)
        XCTAssertEqual(ChargingNetworksForm.parseLocation(" 40.7128, -74.0060 "), .at(40.7128, -74.006))
        XCTAssertEqual(ChargingNetworksForm.parseLocation("40.7128 -74.0060"), .at(40.7128, -74.006))
        XCTAssertEqual(ChargingNetworksForm.parseLocation("91, 0"), .invalid("Latitude must be between -90 and 90."))
        XCTAssertEqual(ChargingNetworksForm.parseLocation("0, 181"), .invalid("Longitude must be between -180 and 180."))
        XCTAssertEqual(ChargingNetworksForm.parseLocation("0, 0"), .invalid("That is 0, 0 (off the coast of Africa); enter the network's location."))
        XCTAssertEqual(ChargingNetworksForm.parseLocation("40.7"), .invalid("Enter a latitude and a longitude, e.g. 40.7128, -74.0060."))
        XCTAssertEqual(ChargingNetworksForm.locationText(ChargingNetwork(name: "x", pricePerKwh: 0, currency: "USD", lat: 40.7128, lon: -74.006)), "40.7128, -74.006")
    }

    func testTheListGoesOutTidyAndTheFirstProblemIsSaidInWords() {
        var d = ChargingNetworksForm.drafts([net("  Ionna ", 0.48, member: 0.36, slug: "ionna"), net("Garage", 0)])
        d[0].net.currency = "usd"; d[0].net.memberActive = true
        d[1].guest = "0,22"; d[1].location = "40.7, -74"
        guard case .success(let out) = ChargingNetworksForm.prepared(d) else { return XCTFail("should be valid") }
        XCTAssertEqual(out[0].name, "Ionna"); XCTAssertEqual(out[0].currency, "USD"); XCTAssertEqual(out[0].memberActive, true)
        XCTAssertEqual(out[1].pricePerKwh, 0.22); XCTAssertEqual(out[1].lat, 40.7); XCTAssertEqual(out[1].lon, -74)

        d[1].location = "nope"
        XCTAssertEqual(ChargingNetworksForm.prepared(d), .failure(.init("Garage: Enter a latitude and a longitude, e.g. 40.7128, -74.0060.")))
        d[1].location = ""; d[1].guest = "9"
        XCTAssertEqual(ChargingNetworksForm.prepared(d), .failure(.init("Garage: Enter a rate from 0 to 5 per kWh.")))
        d[1].guest = "0.2"; d[0].member = "0.5"
        XCTAssertEqual(ChargingNetworksForm.prepared(d), .failure(.init("Ionna: the member rate must be lower than the guest rate.")))
    }

    func testAMemberFlagWithoutAMemberRateIsNotSent() {
        var d = ChargingNetworksForm.drafts([net("Ionna", 0.48, member: 0.36, slug: "ionna")])
        d[0].net.memberActive = true; d[0].member = ""
        guard case .success(let out) = ChargingNetworksForm.prepared(d) else { return XCTFail("valid") }
        XCTAssertNil(out[0].memberPricePerKwh); XCTAssertNil(out[0].memberActive)
    }

    func testSaveIsLiveOnlyForAChangedList() {
        let saved = [net("Ionna", 0.48, member: 0.36, slug: "ionna")]
        var d = ChargingNetworksForm.drafts(saved)
        XCTAssertFalse(ChargingNetworksForm.changed(d, saved: saved))
        d[0].guest = "0.5"
        XCTAssertTrue(ChargingNetworksForm.changed(d, saved: saved))
    }

    func testNetworksDecodeAndEncodeAsTheServerSpellsThem() throws {
        let json = #"[{"name":"Ionna","price_per_kwh":0.48,"currency":"USD","member_price_per_kwh":0.36,"member_plan":"Rewards","slug":"ionna","seeded":true},{"name":"Garage","price_per_kwh":0.2,"currency":"USD","lat":40.7,"lon":-74}]"#
        let rows = try JSON.decoder().decode([ChargingNetwork].self, from: Data(json.utf8))
        XCTAssertEqual(rows[0].memberPricePerKwh, 0.36); XCTAssertEqual(rows[0].seeded, true); XCTAssertEqual(rows[1].lat, 40.7)
        let enc = JSONEncoder(); enc.keyEncodingStrategy = .convertToSnakeCase; enc.outputFormatting = [.sortedKeys]
        let out = String(data: try enc.encode(rows[1]), encoding: .utf8)
        XCTAssertEqual(out, #"{"currency":"USD","lat":40.7,"lon":-74,"name":"Garage","price_per_kwh":0.2}"#)
    }

    func testMembershipTermsGroupByNetworkInTheOrderSentAndLinkOnlyTheNetworksOwnSites() {
        func t(_ slug: String, _ network: String, _ plan: String, _ url: String?, via: String? = "page") -> MembershipTerm {
            MembershipTerm(networkSlug: slug, network: network, plan: plan, terms: "t", sourceUrl: url, readVia: via, checked: "2026-10-20", label: "As listed.")
        }
        let g = ChargingNetworksForm.groupByNetwork([t("evgo", "EVgo", "Plus", nil), t("ea", "Electrify America", "Pass+", nil), t("evgo", "EVgo", "Basic", nil)])
        XCTAssertEqual(g.map(\.network), ["EVgo", "Electrify America"])
        XCTAssertEqual(g[0].plans.map(\.plan), ["Plus", "Basic"])
        XCTAssertNotNil(ChargingNetworksForm.safeSourceURL("https://www.evgo.com/plans"))
        XCTAssertNotNil(ChargingNetworksForm.safeSourceURL("https://electrifyamerica.com/pass"))
        XCTAssertNil(ChargingNetworksForm.safeSourceURL("http://evgo.com/plans"))
        XCTAssertNil(ChargingNetworksForm.safeSourceURL("https://evil-evgo.com/plans"))
        XCTAssertNil(ChargingNetworksForm.safeSourceURL("https://user:pw@evgo.com/plans"))
        XCTAssertNil(ChargingNetworksForm.safeSourceURL(nil))
        XCTAssertEqual(ChargingNetworksForm.readLine(t("a", "A", "p", nil)), "As listed. Read from the network's page.")
        XCTAssertEqual(ChargingNetworksForm.readLine(t("a", "A", "p", nil, via: "snippet")), "As listed. Read from a search snippet.")
        XCTAssertEqual(ChargingNetworksForm.readLine(t("a", "A", "p", nil, via: nil)), "As listed.")
    }

    // MARK: Planner defaults

    private func planner(mode: String = "", adapter: Bool? = nil, gas: Double = 0, mpg: Double = 0) -> PlannerDefaults {
        PlannerDefaults(driveMode: mode, hasAdapter: adapter, gasPricePerGallon: gas, comparisonMpg: mpg, askAdapter: true)
    }

    func testTheAdapterQuestionIsHiddenOnlyWhenEveryTruckIsNativeNacs() {
        XCTAssertTrue(PlannerDefaultsForm.askAdapter(modelYears: nil))
        XCTAssertTrue(PlannerDefaultsForm.askAdapter(modelYears: []))
        XCTAssertTrue(PlannerDefaultsForm.askAdapter(modelYears: [2025]))
        XCTAssertTrue(PlannerDefaultsForm.askAdapter(modelYears: [2026, nil]))
        XCTAssertFalse(PlannerDefaultsForm.askAdapter(modelYears: [2026, 2027]))
    }

    func testTheOptionsAndLabelsAreTheWebs() {
        XCTAssertEqual(PlannerDefaultsForm.driveModes.map(\.title), ["All-Purpose", "Conserve"])
        XCTAssertEqual(PlannerDefaultsForm.Adapter.allCases.map(\.title),
                       ["Not specified (let planner default)", "Yes — I have it", "No — exclude Superchargers"])
        XCTAssertEqual([PlannerDefaultsForm.driveModeLabel, PlannerDefaultsForm.adapterLabel, PlannerDefaultsForm.gasLabel, PlannerDefaultsForm.mpgLabel],
                       ["Default drive mode", "Tesla NACS adapter", "Gas $/gal", "ICE MPG (comparison)"])
    }

    func testOpeningAndSavingWithoutAChangeSendsNothing() {
        let saved = planner(mode: "", adapter: nil, gas: 4, mpg: 20)
        let d = PlannerDefaultsForm.draft(saved)
        XCTAssertEqual(d.driveMode, "EVERYDAY", "nothing chosen reads as the first option, as on the web")
        XCTAssertNil(PlannerDefaultsForm.prefsPatch(d, saved: saved))
        XCTAssertNil(PlannerDefaultsForm.costsPatch(d, saved: saved))
        XCTAssertFalse(PlannerDefaultsForm.changed(d, saved: saved))
    }

    func testOnlyWhatChangedIsSent() {
        let saved = planner(mode: "EVERYDAY", adapter: true, gas: 4, mpg: 20)
        var d = PlannerDefaultsForm.draft(saved)
        d.gas = "4,25"
        XCTAssertNil(PlannerDefaultsForm.prefsPatch(d, saved: saved))
        XCTAssertEqual(PlannerDefaultsForm.costsPatch(d, saved: saved), ChargingRatesPatch(gasPricePerGallon: 4.25))
        d.driveMode = "DISTANCE"; d.adapter = .no; d.mpg = ""
        XCTAssertEqual(PlannerDefaultsForm.prefsPatch(d, saved: saved), PlannerPrefs(driveMode: "DISTANCE", hasAdapter: false))
        XCTAssertEqual(PlannerDefaultsForm.costsPatch(d, saved: saved), ChargingRatesPatch(gasPricePerGallon: 4.25, comparisonMpg: 0))
    }

    func testAnUnreadableNumberIsRefusedInWords() {
        var d = PlannerDefaultsForm.draft(planner())
        d.gas = "four"; d.mpg = "-3"
        XCTAssertEqual(PlannerDefaultsForm.errors(d), [.gas, .mpg])
        XCTAssertEqual(PlannerDefaultsForm.FieldError.gas.message, "Enter a gas price like 4.00 (0 to 100).")
        XCTAssertEqual(PlannerDefaultsForm.FieldError.mpg.message, "Enter miles per gallon like 20 (0 to 500).")
        d.gas = ""; d.mpg = "25"
        XCTAssertTrue(PlannerDefaultsForm.errors(d).isEmpty)
    }

    func testAPatchGoesOutWithOnlyItsFields() throws {
        let enc = JSONEncoder(); enc.keyEncodingStrategy = .convertToSnakeCase; enc.outputFormatting = [.sortedKeys]
        XCTAssertEqual(String(data: try enc.encode(ChargingRatesPatch(gasPricePerGallon: 4.25)), encoding: .utf8), #"{"gas_price_per_gallon":4.25}"#)
        XCTAssertEqual(String(data: try enc.encode(PlannerPrefs(driveMode: "EVERYDAY", hasAdapter: nil)), encoding: .utf8), #"{"drive_mode":"EVERYDAY"}"#)
        let rates = try JSON.decoder().decode(ChargingRates.self, from: Data(#"{"home_price_per_kwh":0.15,"home_currency":"USD","gas_price_per_gallon":3.5,"comparison_mpg":25}"#.utf8))
        XCTAssertEqual(rates.gasPricePerGallon, 3.5); XCTAssertEqual(rates.comparisonMpg, 25)
    }

    // MARK: The server's own answers

    func testTheServersFixturesDecode() throws {
        let off = try Fixture.decode(OffpeakSchedule.self, "settings_offpeak")
        XCTAssertEqual(off.tz, "America/Chicago"); XCTAssertEqual(off.windows.count, 2); XCTAssertEqual(off.windows[1].label, nil)
        XCTAssertEqual(off.baseRate, 0.15); XCTAssertEqual(off.flatRate, false)
        XCTAssertEqual(OffpeakForm.drafts(off).map(\.rate), ["0.09", "0.11"])

        let nets = try Fixture.decode([ChargingNetwork].self, "settings_networks")
        XCTAssertEqual(nets.map(\.name), ["Electrify America", "EVgo", "Corner garage"])
        XCTAssertEqual(nets[0].memberActive, true); XCTAssertEqual(nets[1].seeded, true); XCTAssertEqual(nets[2].lat, 40.7128)
        XCTAssertEqual(ChargingNetworksForm.displayOrder(nets), [0, 2, 1], "preferred first, then by name")
        XCTAssertFalse(ChargingNetworksForm.changed(ChargingNetworksForm.drafts(nets), saved: nets), "opening and saving changes nothing")

        let terms = try Fixture.decode(MembershipTermsResponse.self, "settings_membership_terms")
        XCTAssertEqual(terms.terms?.first?.plan, "Pass+")
        XCTAssertNotNil(ChargingNetworksForm.safeSourceURL(terms.terms?.first?.sourceUrl))

        let prefs = try Fixture.decode(PlannerPrefs.self, "settings_planner")
        XCTAssertEqual(prefs, PlannerPrefs(driveMode: "EVERYDAY", hasAdapter: false))

        let rates = try Fixture.decode(ChargingRates.self, "settings_charging")
        XCTAssertEqual(rates.gasPricePerGallon, 3.5); XCTAssertEqual(rates.comparisonMpg, 25)
    }

    // MARK: The card model

    func testASaveShowsTheServersAnswerNotWhatWasTyped() async {
        let m = SettingsCardModel<Int>(load: .loaded(1))
        await m.save { 7 }
        XCTAssertEqual(m.saved, 7)
        XCTAssertEqual(m.saveState, .saved(7))
        m.clearStatus()
        XCTAssertEqual(m.saveState, .idle)
    }

    func testARefusedSaveLeavesTheSavedValueAndSaysWhy() async {
        let m = SettingsCardModel<Int>(load: .loaded(1))
        await m.save { throw APIError.server(status: 400, message: "windows[0].rate is too high") }
        XCTAssertEqual(m.saved, 1)
        XCTAssertEqual(m.saveState, .failed(.invalid("windows[0].rate is too high")))
        await m.save { throw APIError.server(status: 403, message: "not_available_from_the_app") }
        XCTAssertEqual(m.saveState, .failed(.notAllowed))
        await m.save { throw APIError.transport("offline") }
        XCTAssertEqual(m.saveState, .failed(.unknown))
    }

    func testALoadFailureIsSaidOnceAndTryAgainReads() async {
        let m = SettingsCardModel<Int>()
        await m.load { throw APIError.server(status: 500, message: "x") }
        XCTAssertEqual(m.loadState, .failed)
        await m.load { 3 }
        XCTAssertEqual(m.loadState, .loaded(3))
    }
}
