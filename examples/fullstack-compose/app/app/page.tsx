import { Pool } from "pg";

async function getStatus() {
  const pgStatus = { ok: false, error: "" };
  try {
    const pool = new Pool({ connectionString: process.env.DATABASE_URL });
    await pool.query("SELECT 1");
    await pool.end();
    pgStatus.ok = true;
  } catch (e: unknown) {
    pgStatus.error = e instanceof Error ? e.message : String(e);
  }
  return { pgStatus };
}

export default async function HomePage() {
  const { pgStatus } = await getStatus();

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", padding: "2rem" }}>
      <h1>Nexus Demo — Fullstack Compose Workspace</h1>
      <p>This workspace runs Next.js + Postgres + Redis via Docker Compose.</p>
      <ul style={{ marginTop: "1.5rem", lineHeight: "2" }}>
        <li>
          Postgres:{" "}
          {pgStatus.ok ? (
            <strong style={{ color: "green" }}>Connected ✅</strong>
          ) : (
            <span style={{ color: "red" }}>
              Error ❌ — {pgStatus.error}
            </span>
          )}
        </li>
        <li>
          Redis:{" "}
          <em style={{ color: "#888" }}>
            (checked at compose level via healthcheck)
          </em>
        </li>
      </ul>
      <p style={{ marginTop: "2rem", color: "#666", fontSize: "0.9rem" }}>
        DATABASE_URL: {process.env.DATABASE_URL ?? "(not set)"}
      </p>
    </main>
  );
}
