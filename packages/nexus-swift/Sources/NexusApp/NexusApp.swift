import NexusCore
import SwiftUI

@main
struct NexusApp: App {
    @StateObject private var appState = AppState()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environmentObject(appState)
                .preferredColorScheme(.light)
        }
        .windowToolbarStyle(.unified(showsTitle: false))
        .commands {
            CommandGroup(replacing: .newItem) {
                Button("New Workspace") { appState.showNewWorkspace = true }
                    .keyboardShortcut("n", modifiers: .command)
            }
        }
    }
}
