import XCTest
@testable import NexusCore

final class AsyncDeadlineTests: XCTestCase {

    func testWithSecondsReturnsWhenOperationFinishesUnderDeadline() async throws {
        let v = try await AsyncDeadline.withSeconds(30) {
            7
        }
        XCTAssertEqual(v, 7)
    }

    func testWithSecondsThrowsExceededWhenOperationSleepsPastDeadline() async throws {
        do {
            _ = try await AsyncDeadline.withSeconds(1) {
                try await Task.sleep(nanoseconds: 10_000_000_000)
                return 0
            }
            XCTFail("expected AsyncDeadlineError.exceeded")
        } catch AsyncDeadlineError.exceeded(seconds: let s) {
            XCTAssertEqual(s, 1)
        }
    }
}
