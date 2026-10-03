# hub-hooks

Dogfood hooks for the nexus session hub, registered by `../hooks/hooks.json`.

- `session-start.sh` (SessionStart): `nexus hub hello --shell`, appends `NEXUS_HUB_SESSION`/`NEXUS_HUB_SEAT` to `$CLAUDE_ENV_FILE`, injects the digest.
- `prompt-submit.sh` (UserPromptSubmit): `nexus hub inbox --seat $NEXUS_HUB_SEAT --since-cursor --ack`, injected as `<nexus-hub-event not-user-input>` with a not-from-the-user preamble.

Both are silent no-ops (exit 0, no output) when `nexus` is missing, has no `hub` verb, any command fails or prints nothing, `jq` and `python3` are both absent, or `NEXUS_HUB_DELIVERY=mod` (the mod delivers instead). Event text is only ever passed as data to `jq`/`python3`; `<` is replaced so it cannot close the frame.

Test: `bash plugins/claude/hub-hooks/test.sh`
