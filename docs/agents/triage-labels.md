# Triage Labels

The skills speak in terms of five canonical triage roles. This file maps those roles to the actual label strings used in this repo's issue tracker.

## State roles

| Label in mattpocock/skills | Label in our tracker | Meaning                                  |
| -------------------------- | -------------------- | ---------------------------------------- |
| `needs-triage`             | `needs-triage`       | Maintainer needs to evaluate this issue  |
| `needs-info`               | `needs-info`         | Waiting on reporter for more information |
| `ready-for-agent`          | `ready-for-agent`    | Fully specified, ready for an AFK agent  |
| `ready-for-human`          | `ready-for-human`    | Requires human implementation            |
| `wontfix`                  | `wontfix`            | Will not be actioned                     |

## Category roles

Every triaged issue carries exactly one category role and one state role.

| Category    | Label in our tracker | Meaning                    |
| ----------- | -------------------- | -------------------------- |
| `bug`       | `bug`                | Something is broken        |
| `enhancement` | `enhancement`       | New feature or improvement |

If state roles conflict, flag it and ask the maintainer before doing anything else.

## Scope

These apply to **freshly filed intake issues only**. Do not apply them to issues carrying a `wayfinder:*` label — those are planning decisions, and `wayfinder/SKILL.md` reserves those labels exclusively. See `issue-tracker.md`.

Edit the "Label in our tracker" columns to match whatever vocabulary you actually use.