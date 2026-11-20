import XCTest

extension XCUIElement {
    /// A chip's selected state follows its tap by a render; a slow CI simulator showed it a beat late (PlanSheetUITests failed once with "the day
    /// chip took the tap" and passed on the re-run). 20 s like the other waits: a pass returns at once, a lost tap still fails.
    func becomesSelected(_ selected: Bool = true, timeout: TimeInterval = 20) -> Bool {
        let p = NSPredicate(format: "isSelected == %@", NSNumber(value: selected))
        return XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: p, object: self)], timeout: timeout) == .completed
    }
}
