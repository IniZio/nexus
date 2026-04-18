export default function HomePage() {
  const workspaceName = process.env.NEXUS_WORKSPACE_NAME ?? "(not set)";
  const workspaceId = process.env.NEXUS_WORKSPACE_ID ?? "(not set)";

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", padding: "2rem" }}>
      <h1>Nexus Demo — Next.js Workspace</h1>
      <p>This app is running inside a Nexus workspace.</p>
      <table style={{ borderCollapse: "collapse", marginTop: "1rem" }}>
        <tbody>
          <tr>
            <td style={{ padding: "4px 12px 4px 0", fontWeight: "bold" }}>NEXUS_WORKSPACE_NAME</td>
            <td style={{ padding: "4px 0" }}>{workspaceName}</td>
          </tr>
          <tr>
            <td style={{ padding: "4px 12px 4px 0", fontWeight: "bold" }}>NEXUS_WORKSPACE_ID</td>
            <td style={{ padding: "4px 0" }}>{workspaceId}</td>
          </tr>
        </tbody>
      </table>
    </main>
  );
}
