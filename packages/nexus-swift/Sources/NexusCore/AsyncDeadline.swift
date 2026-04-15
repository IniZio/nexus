import Foundation

/// Thrown when `withSeconds` elapses before `operation` completes.
public enum AsyncDeadlineError: Error, Equatable {
    case exceeded(seconds: UInt64)
}

/// Races `operation` against a sleep; cancels the group when the first branch finishes.
/// Used for startup and daemon-side-effect caps so the UI cannot hang indefinitely.
public enum AsyncDeadline {
    public static func withSeconds<T>(
        _ seconds: UInt64,
        operation: @escaping () async throws -> T
    ) async throws -> T {
        try await withThrowingTaskGroup(of: T.self) { group in
            group.addTask { try await operation() }
            group.addTask {
                try await Task.sleep(nanoseconds: seconds * 1_000_000_000)
                throw AsyncDeadlineError.exceeded(seconds: seconds)
            }
            defer { group.cancelAll() }
            return try await group.next()!
        }
    }
}
