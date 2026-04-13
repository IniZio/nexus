// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "NexusApp",
    platforms: [.macOS(.v14)],
    dependencies: [
        .package(url: "https://github.com/migueldeicaza/SwiftTerm.git", from: "1.2.0"),
    ],
    targets: [
        // ── Core library: all business logic, models, client ──────────
        .target(
            name: "NexusCore",
            path: "Sources/NexusCore",
            swiftSettings: [
                .enableExperimentalFeature("StrictConcurrency"),
            ]
        ),

        // ── App executable: SwiftUI entry point + views ───────────────
        .executableTarget(
            name: "NexusApp",
            dependencies: [
                "NexusCore",
                .product(name: "SwiftTerm", package: "SwiftTerm"),
            ],
            path: "Sources/NexusApp",
            swiftSettings: [
                .enableExperimentalFeature("StrictConcurrency"),
            ]
        ),

        // ── Integration test suite ────────────────────────────────────
        .testTarget(
            name: "NexusAppTests",
            dependencies: ["NexusCore"],
            path: "Tests/NexusAppTests"
        ),
    ]
)
