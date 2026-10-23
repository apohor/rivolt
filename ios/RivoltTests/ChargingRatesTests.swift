import SwiftUI
import XCTest
@testable import Rivolt

final class ChargingRatesTests: XCTestCase {
    // MARK: What is a rate

    private func rate(_ s: String) -> Double? { try? ChargingRatesForm.parse(s).get() }
    private func error(_ s: String) -> ChargingRatesForm.RateError? {
        if case .failure(let e) = ChargingRatesForm.parse(s) { return e }
        return nil
    }

    func testARateIsDigitsWithOneDecimalMark() {
        XCTAssertEqual(rate("0.15"), 0.15)
        XCTAssertEqual(rate("0,15"), 0.15, "a comma keyboard")
        XCTAssertEqual(rate(" .5 "), 0.5)
        XCTAssertEqual(rate("5"), 5)
        XCTAssertEqual(rate("0.1234"), 0.1234)
        XCTAssertEqual(rate(""), 0, "empty is not set, as on the web")
        XCTAssertEqual(rate("   "), 0)
        XCTAssertEqual(error("6"), .outOfRange)
        XCTAssertEqual(error("5.0001"), .outOfRange)
        XCTAssertEqual(error("0.12345"), .tooManyDecimals)
        for bad in ["-1", "1e2", "1.2.3", "1,234.5", "abc", ".", ",", "٣", "0.1 5", "$0.15"] {
            XCTAssertEqual(error(bad), .notANumber, bad)
        }
    }

    func testTheFieldShowsTheRateInTheKeyboardsDecimalMark() {
        let us = Locale(identifier: "en_US"), de = Locale(identifier: "de_DE")
        XCTAssertEqual(ChargingRatesForm.fieldText(0.15, locale: us), "0.15")
        XCTAssertEqual(ChargingRatesForm.fieldText(0.15, locale: de), "0,15")
        XCTAssertEqual(ChargingRatesForm.fieldText(0.1, locale: us), "0.1")
        XCTAssertEqual(ChargingRatesForm.fieldText(2, locale: us), "2")
        XCTAssertEqual(ChargingRatesForm.fieldText(0, locale: us), "", "not set: an empty field")
        // What is shown parses back to the same rate.
        for r in [0.15, 0.1234, 0.5, 4.9999, 3] { XCTAssertEqual(rate(ChargingRatesForm.fieldText(r, locale: de)), r) }
    }

    func testOnlyWhatChangedIsSent() {
        let saved = ChargingRates(homePricePerKwh: 0.15, homeCurrency: "USD")
        XCTAssertNil(ChargingRatesForm.patch(rate: 0.15, currency: "USD", saved: saved), "nothing changed")
        XCTAssertNil(ChargingRatesForm.patch(rate: 0.15, currency: "usd", saved: saved), "case is not a change")
        XCTAssertEqual(ChargingRatesForm.patch(rate: 0.2, currency: "USD", saved: saved), ChargingRatesPatch(homePricePerKwh: 0.2, homeCurrency: nil))
        XCTAssertEqual(ChargingRatesForm.patch(rate: 0.15, currency: "eur", saved: saved), ChargingRatesPatch(homePricePerKwh: nil, homeCurrency: "EUR"))
        XCTAssertEqual(ChargingRatesForm.patch(rate: 0, currency: "CAD", saved: saved), ChargingRatesPatch(homePricePerKwh: 0, homeCurrency: "CAD"), "0 turns it off and is sent")
    }

    func testThePatchBodyHasOnlyTheKeysItCarries() throws {
        let enc = JSONEncoder()
        enc.keyEncodingStrategy = .convertToSnakeCase
        enc.outputFormatting = [.sortedKeys]
        func body(_ p: ChargingRatesPatch) throws -> String { String(decoding: try enc.encode(p), as: UTF8.self) }
        XCTAssertEqual(try body(ChargingRatesPatch(homePricePerKwh: 0.15, homeCurrency: nil)), #"{"home_price_per_kwh":0.15}"#)
        XCTAssertEqual(try body(ChargingRatesPatch(homePricePerKwh: nil, homeCurrency: "EUR")), #"{"home_currency":"EUR"}"#)
        XCTAssertEqual(try body(ChargingRatesPatch(homePricePerKwh: 0.2, homeCurrency: "GBP")), #"{"home_currency":"GBP","home_price_per_kwh":0.2}"#)
    }

    func testTheRatesDecodeOnlyTheHomeRateNotTheGasPriceOrMPG() throws {
        let json = #"{"home_price_per_kwh":0.15,"home_currency":"USD","gas_price_per_gallon":4,"comparison_mpg":20}"#
        let r = try JSON.decoder().decode(ChargingRates.self, from: Data(json.utf8))
        XCTAssertEqual(r, ChargingRates(homePricePerKwh: 0.15, homeCurrency: "USD"))
        XCTAssertEqual(Mirror(reflecting: r).children.compactMap(\.label), ["homePricePerKwh", "homeCurrency"])
        XCTAssertEqual(ChargingRatesForm.currencies.count, 10)
    }

    func testTheSavedRateIsSaidInTheServersCurrency() {
        XCTAssertEqual(ChargingRatesForm.summary(.init(homePricePerKwh: 0.15, homeCurrency: "USD")), "$0.150/kWh")
        XCTAssertEqual(ChargingRatesForm.summary(.init(homePricePerKwh: 0, homeCurrency: "USD")), "No home rate set")
        XCTAssertEqual(ChargingRatesForm.spokenSummary(.init(homePricePerKwh: 0.15, homeCurrency: "USD")), "$0.150 per kilowatt hour")
    }

    // MARK: The model

    private func err(_ status: Int, _ message: String = "m") -> Error { APIError.server(status: status, message: message) }

    func testEachWayASaveFailsHasItsOwnHonestWords() {
        typealias M = ChargingRatesModel
        XCTAssertEqual(M.saveError(err(400, "home_price_per_kwh must be a number from 0 to 5")), .invalid("home_price_per_kwh must be a number from 0 to 5"))
        XCTAssertEqual(M.saveError(err(403, "this action is not available from the iPhone app")), .notAllowed)
        XCTAssertEqual(M.saveError(err(404)), .notAllowed, "a server with no such route")
        XCTAssertEqual(M.saveError(err(405)), .notAllowed)
        XCTAssertEqual(M.saveError(err(429)), .tooMany)
        XCTAssertEqual(M.saveError(err(500)), .serverError)
        XCTAssertEqual(M.saveError(err(503)), .serverError)
        XCTAssertEqual(M.saveError(URLError(.timedOut)), .unknown, "no answer: whether it saved is unknown")
        XCTAssertEqual(M.saveError(APIError.transport("offline")), .unknown)
        XCTAssertTrue(M.SaveError.unknown.message.contains("can't say whether it saved"))
        XCTAssertTrue(M.SaveError.notAllowed.message.contains("Change it on the web"))
        XCTAssertTrue(M.SaveError.tooMany.message.contains("Wait a little"))
    }

    @MainActor
    func testTheModelReadsOnceAndSavesWhatChangedAndShowsTheServersAnswer() async throws {
        let source = FakeRates(read: .success(.init(homePricePerKwh: 0.15, homeCurrency: "USD")),
                               write: .success(.init(homePricePerKwh: 0.2, homeCurrency: "USD")))
        let model = ChargingRatesModel()
        await model.load(source: source)
        XCTAssertEqual(model.saved, .init(homePricePerKwh: 0.15, homeCurrency: "USD"))
        await model.save(source: source, pricePerKwh: 0.2, currency: nil)
        XCTAssertEqual(source.writes.count, 1)
        XCTAssertEqual(source.writes[0].rate, 0.2)
        XCTAssertNil(source.writes[0].currency, "only the changed field")
        XCTAssertEqual(model.saveState, .saved(.init(homePricePerKwh: 0.2, homeCurrency: "USD")))
        XCTAssertEqual(model.saved?.homePricePerKwh, 0.2, "the saved rate is the server's answer")
    }

    @MainActor
    func testARefusedSaveKeepsTheSavedRateAndIsNotRetried() async throws {
        let source = FakeRates(read: .success(.init(homePricePerKwh: 0.15, homeCurrency: "USD")), write: .failure(err(429)))
        let model = ChargingRatesModel()
        await model.load(source: source)
        await model.save(source: source, pricePerKwh: 0.3, currency: "EUR")
        XCTAssertEqual(model.saveState, .failed(.tooMany))
        XCTAssertEqual(model.saved, .init(homePricePerKwh: 0.15, homeCurrency: "USD"), "what is shown as saved is still the last saved rate")
        XCTAssertEqual(source.writes.count, 1, "never retried in a loop")
    }

    @MainActor
    func testAFailedReadSaysSoAndReloadingClearsAnOldResult() async throws {
        let bad = FakeRates(read: .failure(URLError(.notConnectedToInternet)), write: .failure(err(500)))
        let model = ChargingRatesModel()
        await model.load(source: bad)
        XCTAssertEqual(model.loadState, .failed)
        let good = FakeRates(read: .success(.init(homePricePerKwh: 0, homeCurrency: "USD")), write: .failure(err(500)))
        await model.load(source: good)
        XCTAssertEqual(model.saved?.homePricePerKwh, 0)
        XCTAssertEqual(model.saveState, .idle)
    }
}

private final class FakeRates: ChargingRatesSource, @unchecked Sendable {
    struct Write { let rate: Double?; let currency: String? }
    private let read: Result<ChargingRates, Error>
    private let write: Result<ChargingRates, Error>
    private let lock = NSLock()
    private var _writes: [Write] = []
    var writes: [Write] { lock.withLock { _writes } }
    init(read: Result<ChargingRates, Error>, write: Result<ChargingRates, Error>) { self.read = read; self.write = write }
    func chargingRates() async throws -> ChargingRates { try read.get() }
    func setChargingRates(pricePerKwh: Double?, currency: String?) async throws -> ChargingRates {
        record(Write(rate: pricePerKwh, currency: currency))
        return try write.get()
    }
    private func record(_ w: Write) { lock.withLock { _writes.append(w) } }
}
