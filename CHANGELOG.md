# Changelog

## [0.4.3](https://github.com/SteerSpec/strspc-sync/compare/v0.4.2...v0.4.3) (2026-11-07)


### Bug Fixes

* **sync:** commit through GraphQL so token auth passes Signed Commit rulesets ([bd5e967](https://github.com/SteerSpec/strspc-sync/commit/bd5e967ec2d45cad1b26a31b1b3c436fa5f5bb63))
* **sync:** commit through GraphQL so token auth passes Signed Commit rulesets ([f7a9a1c](https://github.com/SteerSpec/strspc-sync/commit/f7a9a1cac181bec367cab6642d896a48f8d18139)), closes [#126](https://github.com/SteerSpec/strspc-sync/issues/126)
* **sync:** stop when the sync-branch read fails for a reason other than 404 ([4abc4e5](https://github.com/SteerSpec/strspc-sync/commit/4abc4e521ea128901202c94a604748ee0a3bb695)), closes [#126](https://github.com/SteerSpec/strspc-sync/issues/126)


### Documentation

* **readme:** center the header like the other SteerSpec repos ([f16de2b](https://github.com/SteerSpec/strspc-sync/commit/f16de2bfa7b05cc9cf1fb62ee678527ea674a216))
* **readme:** center the header like the other SteerSpec repos ([b79a142](https://github.com/SteerSpec/strspc-sync/commit/b79a1422ef62d517cfe8059012369c4eddca4a5e))

## [0.4.2](https://github.com/SteerSpec/strspc-sync/compare/v0.4.1...v0.4.2) (2026-11-06)


### Documentation

* address review (copyright appendix, action pins, force wording, conflict outputs) ([aee28a1](https://github.com/SteerSpec/strspc-sync/commit/aee28a1735a2857a81c867f476e5fb2175f3226f))
* name Consulare in NOTICE, link it from Governance, add tagged source links ([1cde013](https://github.com/SteerSpec/strspc-sync/commit/1cde0135c4a2bb29c5e727cd8320297970a9647a))
* qualify when a sync pull request opens ([05545f3](https://github.com/SteerSpec/strspc-sync/commit/05545f307de3e8329d07348d88f51b79c843a1b6))
* **readme:** add logo, state the licence once ([09336cf](https://github.com/SteerSpec/strspc-sync/commit/09336cfc29e394ebe9623fe20aaadfb5d083bfc5))
* **readme:** align with README and BRAND rules, refresh pins to v0.4.1 ([0b0433a](https://github.com/SteerSpec/strspc-sync/commit/0b0433ad3ad1eb91a6ea4d42e29a7ae870aa4e7e))
* **readme:** drop version prose, move auth detail to docs, sharpen wording ([c99b2c1](https://github.com/SteerSpec/strspc-sync/commit/c99b2c1f37927156311f44df4b7120d93b9f0839))
* **readme:** move reference content to docs/ and align with README rules ([b1dd1b4](https://github.com/SteerSpec/strspc-sync/commit/b1dd1b4a58b3f8041c51386415c4efb5eacb68c6))
* **readme:** move reference content to docs/, add governance, drop Go badge ([5d4d13d](https://github.com/SteerSpec/strspc-sync/commit/5d4d13d0bfad16b0c9afe9a25653246678133f7a))
* **readme:** order security before community, drop unmeasured claim, palette logo colour ([d901f3f](https://github.com/SteerSpec/strspc-sync/commit/d901f3fc75d57c4e2c33282a454e24d6d7dd135f))
* **readme:** qualify approval, check and force-push claims ([2c09f2b](https://github.com/SteerSpec/strspc-sync/commit/2c09f2b28fcbc8652065a13416ffd182dce35f51))
* **readme:** rewrap long lines ([10a423a](https://github.com/SteerSpec/strspc-sync/commit/10a423abd6051a71f24c5d04570a6dff9deff7ce))
* show the transparent logo ([4609e46](https://github.com/SteerSpec/strspc-sync/commit/4609e4645563fa8a4186a10e5fc829b646b69e81))
* tighten sync-flow wording, link CI workflow at v0.4.1 ([19e03d2](https://github.com/SteerSpec/strspc-sync/commit/19e03d2f46e29f27d038aa586499761286c657a0))
* wrap long line ([abd5708](https://github.com/SteerSpec/strspc-sync/commit/abd570822ea19c429330a19b51eb0428c0ba3a29))
* wrap long lines, drop the platform claim, list the conflict summary output ([9cab316](https://github.com/SteerSpec/strspc-sync/commit/9cab316225aaa19c94d9fe8892d55b9e0ecc3fa4))

## [0.4.1](https://github.com/SteerSpec/strspc-sync/compare/v0.4.0...v0.4.1) (2026-11-01)


### Bug Fixes

* **rules:** move inline examples into example notes ([7b79877](https://github.com/SteerSpec/strspc-sync/commit/7b79877b7c19051b1581f5c66fff4c3b82eaec06))
* **rules:** move inline examples into example notes ([56f4891](https://github.com/SteerSpec/strspc-sync/commit/56f4891c0a4b4393d371d7b2b1b3b424b9e02144))

## [0.4.0](https://github.com/SteerSpec/strspc-sync/compare/v0.3.0...v0.4.0) (2026-10-30)


### Features

* **e2e:** add the e2e test harness (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([4d700c3](https://github.com/SteerSpec/strspc-sync/commit/4d700c32cd077de1eddc40cbe9c54f58fc7e2ba3))


### Bug Fixes

* **action:** pass inputs to strspc as quoted arguments, not shell text ([a23af57](https://github.com/SteerSpec/strspc-sync/commit/a23af577e9464367787b0023e846499036f7c480))
* **action:** pass inputs to strspc as quoted arguments, not shell text ([e92c00c](https://github.com/SteerSpec/strspc-sync/commit/e92c00c24859bf66da1554cc2ce20c84d68bd83e))
* **e2e:** address Copilot review of wave 1 (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([d3bb6b3](https://github.com/SteerSpec/strspc-sync/commit/d3bb6b39223d5eb3a73935a526a98093487c468a))
* **e2e:** apply E2E_ORG to the org_name variable (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([4460aba](https://github.com/SteerSpec/strspc-sync/commit/4460aba36d8208513887d223a06d768942163980))
* **e2e:** delete files with a signed GraphQL commit (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([7f153eb](https://github.com/SteerSpec/strspc-sync/commit/7f153eb2c724fa4675a68d31d5d51ade3640d71c))
* **e2e:** delete files with a signed GraphQL commit (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([0d7d1af](https://github.com/SteerSpec/strspc-sync/commit/0d7d1af25e349578213c75808563cead02a2cbd5))
* **e2e:** fail on missing secrets, confirm 422 branch deletes, probe at the deadline (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([239835d](https://github.com/SteerSpec/strspc-sync/commit/239835df30658c35acc7541da007cdf40ed0047c))
* **e2e:** give each test its own run prefix (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([b193524](https://github.com/SteerSpec/strspc-sync/commit/b1935248c1ab5b819f15acedc1b8b112f913f636))
* **e2e:** reset state and sweep labelled issues at cleanup (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([a6a2dd5](https://github.com/SteerSpec/strspc-sync/commit/a6a2dd50876bd1aa526341dd48bfaad3a9ae0625))
* **e2e:** scope the symlink test's PR check to its own prefix (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([ec9b184](https://github.com/SteerSpec/strspc-sync/commit/ec9b1840f2761ef31644d576f8047f8308a3b652))


### Documentation

* **e2e:** record the live runs and the mutation check (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([a84b2d2](https://github.com/SteerSpec/strspc-sync/commit/a84b2d2fc00c2bf792d54efec25de0fb9e8aaa35))
* **e2e:** record the live runs and the mutation check (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([7595ada](https://github.com/SteerSpec/strspc-sync/commit/7595adab1eac1f2c0d22ff6d6b03190ebf93857c))
* **e2e:** the green run passed 10 scenarios, not 9 (GH [#99](https://github.com/SteerSpec/strspc-sync/issues/99)) ([8575e36](https://github.com/SteerSpec/strspc-sync/commit/8575e36a0467cd719e9f05dcf21498a5854cd65e))
* fix markdownlint MD060 table style ([75a58da](https://github.com/SteerSpec/strspc-sync/commit/75a58da68caaafd083fbd713e054a87b9551ce8d))
* use compact table delimiter rows for markdownlint MD060 ([33cf967](https://github.com/SteerSpec/strspc-sync/commit/33cf9674fc3a7d38b02de8b922675bf99a2d0c60))
