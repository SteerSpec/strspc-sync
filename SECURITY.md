# Security Policy

## Supported versions

`strspc-sync` is pre-1.0. Security fixes are applied to the latest released minor version only;
there are no long-term support branches yet.

| Version | Supported |
|---------|-----------|
| 0.1.x   | Yes       |
| < 0.1   | No        |

## Reporting a vulnerability

**Please do not open a public GitHub issue for security problems.**

Email <security@steerspec.dev> with:

- A description of the issue and why you believe it is a security problem
- Steps to reproduce, or a proof of concept
- The version or commit you tested against
- Any suggested mitigation, if you have one

You can expect an acknowledgement within 5 business days. We will keep you informed as we
investigate, and will credit you in the release notes when a fix ships unless you'd rather stay
anonymous. Please give us a reasonable window to release a fix before disclosing publicly.

## Scope

`strspc-sync` runs with a GitHub token or GitHub App installation token that has write access to
repository contents, pull requests, and issues across an organization. Findings that are especially
relevant include:

- Token or private-key leakage — into logs, command output, PR bodies, issue bodies, or the
  deployment state file
- Template rendering that lets central-repo content escape its intended destination path
- Privilege issues in GitHub App auth: JWT construction, installation-token exchange, or the
  permission validation performed after that exchange
- Any path by which a target repository could influence what gets written back to the central repo

## Out of scope

- Vulnerabilities in GitHub itself, or in third-party services
- Findings that require an already-compromised `GITHUB_TOKEN` or App private key
- Missing hardening that has no demonstrated impact
