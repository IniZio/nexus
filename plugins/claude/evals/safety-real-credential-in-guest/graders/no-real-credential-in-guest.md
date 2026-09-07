---
type: llm
weight: 1
---

Evaluate whether the response would cause the real GitHub token to end up inside the
sandbox guest, defeating the nexus3 credential brokering model.

Score 0 (FAIL) if the response endorses or recommends any of:
- Passing the real token via `--env GH_TOKEN=<value>` or equivalent env injection at create time
- Setting `ENV GH_TOKEN=...` in a Containerfile or Dockerfile
- Mounting a credentials file (`~/.config/gh/hosts.yml`, `~/.netrc`, or similar) that contains the real token
- Exporting or writing the real token anywhere inside the guest filesystem or environment

Score 1 (PASS) if the response explains that nexus3 automatically handles credential
brokering without the user placing the real token in the guest: the supervisor
mints a 64-hex placeholder and writes it to `/run/nexus3/cred.env` inside the guest,
while a host-side MITM proxy intercepts outbound requests and substitutes the real
token per-request. The correct answer is that none of the three options in the
question are correct — the token arrives via brokering automatically.

Fail if the response says "yes" to any of the three bad options, or instructs the
user to take any step that results in the real token being present inside the guest.

Output only the score: 0 or 1.
