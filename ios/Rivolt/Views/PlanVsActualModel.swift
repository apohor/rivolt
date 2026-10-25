import Foundation
import Observation

/// The plan-vs-actual calls, kept apart from `APIClient` so a test can see what is sent.
protocol PlanVsActualSource: Sendable {
    func planVsActual(savedID: String) async throws -> PlanVsActualAnswer
    func linkPlanVsActual(savedID: String, _ body: PlanVsActualLinkBody) async throws
    func unlinkPlanVsActual(savedID: String, _ body: PlanVsActualLinkBody) async throws
}

extension APIClient: PlanVsActualSource {}

/// One saved trip against the outing that was driven: read it, confirm a suggested drive, or say it was not this trip. Nothing is
/// retried on its own, and a link or unlink is followed by a fresh read, so what shows is what the server holds.
@MainActor
@Observable
final class PlanVsActualModel {
    enum Phase: Equatable {
        case idle
        case loading
        case loaded([PlanVsActual])
        case failed(String)
    }

    private(set) var phase = Phase.idle
    private(set) var busy = false
    private(set) var message: String?

    func load(source: some PlanVsActualSource, savedID: String) async {
        guard !busy else { return }
        phase = .loading
        message = nil
        await read(source: source, savedID: savedID)
    }

    private func read(source: some PlanVsActualSource, savedID: String) async {
        do {
            phase = .loaded(try await source.planVsActual(savedID: savedID).parts)
        } catch {
            if Task.isCancelled { phase = .idle; return }
            phase = .failed(PlanVsActualLogic.failure(error))
        }
    }

    func link(source: some PlanVsActualSource, savedID: String, tripID: String, day: Int?) async {
        await change(source: source, savedID: savedID) { try await source.linkPlanVsActual(savedID: savedID, PlanVsActualLinkBody(tripId: tripID, day: day)) }
    }

    func unlink(source: some PlanVsActualSource, savedID: String, tripID: String, day: Int?) async {
        await change(source: source, savedID: savedID) { try await source.unlinkPlanVsActual(savedID: savedID, PlanVsActualLinkBody(tripId: tripID, day: day)) }
    }

    private func change(source: some PlanVsActualSource, savedID: String, _ call: () async throws -> Void) async {
        guard !busy else { return }
        busy = true
        message = nil
        defer { busy = false }
        do {
            try await call()
        } catch {
            if !Task.isCancelled { message = PlanVsActualLogic.failure(error) }
            return
        }
        await read(source: source, savedID: savedID)
    }
}
