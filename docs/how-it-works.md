# How it works

Templates live in one central repo. On every run, `strspc` renders each template for each target
repository, hashes the result with BLAKE3, and compares it against the recorded deployment state.
Only genuine changes become pull requests — an unchanged render is skipped and opens no pull request.

```mermaid
flowchart LR
    C["Central repo<br/><i>templates + steerspec-sync.yml</i>"]
    R["Render<br/><i>mustache · marker · full-replace</i>"]
    D{"Content<br/>changed?"}
    X["Skip"]
    P["Pull request<br/><i>one per template, per repo</i>"]
    T["Target repos<br/><i>CLAUDE.md · agents · skills · settings</i>"]
    S["Deployment state<br/><i>BLAKE3 hashes</i>"]

    C --> R --> D
    D -->|no| X
    D -->|yes| P --> T
    P --> S
    S -.->|compared on next run| D
```

Each pull request in a target repo is titled `chore(steerspec-sync): update <template-id> to <version>` (the type
and scope are configurable) so commitlint and semantic-PR checks let it through. Two companion commands watch what
happens afterwards — `monitor` reports files that have drifted away from their template, and `conflict` flags
contradictions across the fleet.
