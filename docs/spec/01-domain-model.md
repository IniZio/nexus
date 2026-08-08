# 01 — Domain Model

*Purpose: the ubiquitous language — entities, identity, artifacts, and the host/client distinction that every other doc binds to.*

## Ubiquitous language

These nouns are fixed. Reuse them verbatim; do not introduce synonyms.

### `Sandbox` — the one first-class entity

`Project` and `Sandbox` collapse into **one entity: `Sandbox`** (ticket 10). There is no separate `Workspace` — that noun is retired (herdr owns "workspace" as a pane concept, and the term is industry-overloaded). `Sandbox` is chosen because the map's own thesis says *sandbox policy core* and ticket 20 is the *sandbox policy model*.

A `Sandbox` is durable: it exists from `create` until an explicit `rm`. Its running VM is **not** modelled as a separate entity. The durable/running split is **deliberately not made** (ticket 10), with a recorded trigger to split into `Sandbox` + `VM` only if one Sandbox ever needs more than one concurrent running instantiation. The split is kept cheap to make later by holding the run identifier as an **internal field** (`instance_id`, below) and **never letting `sandbox_id` key runtime-scoped resources**.

A **fan-out child** produced by `fork` is an **ordinary `Sandbox`** on the identical machine — its own id, its own record, the same lifecycle states — distinguished only by a **provenance field**, never by a state and never by a second entity type (ticket 19 ruling 14).

### Artifacts

- **`Base`** / **`Image`** — ticket 14's words. A `Base` is the OCI base image; an `Image` is the built rootfs (builder VM output, raw ext4). Content-addressed.
- **`Snapshot`** — a first-class artifact (ticket 13): the saved memory + device state of a running VM, with its own durable record and retention rules. Content-addressed. Non-portable across hosts by policy.

`fork`, `restore`, `attach`, `pause`, `resume`, `cp` are **operations**, not types.

## Identity

- **Sandbox id:** `sb-` + **Crockford base32 UUIDv7**. Time-ordered, **prefix-matchable** (`nexus3 stop sb-3f9` resolves if unambiguous).
- **Human name — the primary handle:** `<project>/<name>` (e.g. `nexus3/feature-x`). This is what users type and what appears in listings; the `sb-` id is the stable machine key.
- **Internal `instance_id`:** a per-running-instantiation identifier, held as a **field on the Sandbox record, never used to key runtime-scoped resources** and never exposed as a handle. It is what keeps the durable/running split convertible (ticket 10) and it is the host-side session key component `(instance_id, session_id)` (ticket 16, doc 04). Renamed from ticket 16's earlier `clone_id`.

### Content-addressing

Content-addressing applies to **`Project` identity, `Image` and `Snapshot`** — inputs that are meant to deduplicate. It is **rejected for `Sandbox`** (ticket 10): fan-out exists precisely to make N sandboxes from identical inputs, so content-addressing would collide them into one. A Sandbox's identity is its `sb-` id, not a digest of its inputs.

`Project` identity is the surviving half of the retired "deterministic addressing" capability (ticket 10) — a content hash over the normalized project, and nothing more. No network identity is derived from any id.

## Host vs client

Two distinct terms (ticket 10; map Notes):

- **host** — the machine that runs nexus3, the VMM and the sandbox VM.
- **client** — where the user's terminal, editor and browser live.

They **coincide** in the common local case and **diverge** the moment a client drives sandboxes on a remote host. Older map text used "host" for both; this spec keeps them separate. The `--remote=user@host` shape (codebox's model) is the same UX against a remote host — the host is configuration, not a separate code path — though remote-host operation beyond SSH-endpoint access is not a v1 surface deliverable.

---

*Sources: tickets 10 (entities, identity, host/client, content-addressing, instance_id), 13 (Snapshot artifact), 14 (Base/Image), 19 (fan-out child as ordinary Sandbox), 16 (instance_id keying), 30 (`clone` names provenance). Map Corrections: deterministic-addressing retired, only content-addressed Project survives.*
