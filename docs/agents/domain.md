# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

## Before exploring, read these

- **`GLOSSARY.md`** at the repo root.
- **`docs/adr/`**: read ADRs that touch the area you're about to work in.

If any of these files don't exist, **proceed silently**. Don't flag their absence; don't suggest creating them upfront. The `/domain-modeling` skill (reached via `/grill-with-docs` and `/improve-codebase-architecture`) creates them lazily when terms or decisions actually get resolved.

Note: `triage` calls `domain-modeling` during its grilling step, so it will create these on first use even though `domain-modeling` is not currently installed.

## File structure

This is a **single-context** repo:

```
/
├── GLOSSARY.md
├── docs/
│   ├── agents/                  ← skill configuration
│   │   ├── issue-tracker.md
│   │   ├── triage-labels.md
│   │   └── domain.md
│   └── adr/
└── workers/
    ├── general/Dockerfile
    ├── ml/{Dockerfile,entrypoint.py}
    ├── spatial/Dockerfile
    ├── shared/{entrypoint.py,storage.py}
    └── testdata/
```

Design currently lives in `README.md` and the `*.md` plan files at the root (`spatial_engine_plan.md`, `wise_orchestrator_handoff.md` — note these are `.gitignore`d via `*plan.md`). When a decision is settled, it belongs in `docs/adr/`, not only in a plan file.

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a test name), use the term as defined in `GLOSSARY.md`. Don't drift to synonyms the glossary explicitly avoids.

If the concept you need isn't in the glossary yet, that's a signal: either you're inventing language the project doesn't use (reconsider) or there's a real gap (note it for `/domain-modeling`).

Terms already in wide use in this repo, and which belong in `GLOSSARY.md`: **control plane**, **worker**, **engine** (`general`/`ml`/`spatial`), **job**, **ephemeral worker**, **store-free mode**, **engine registry**.

## Flag ADR conflicts

If your output contradicts an existing ADR, surface it explicitly rather than silently overriding:

> _Contradicts ADR-0007 (event-sourced orders), but worth reopening because…_