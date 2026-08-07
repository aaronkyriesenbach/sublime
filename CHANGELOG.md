# Changelog

## [0.3.0](https://github.com/aaronkyriesenbach/sublime/compare/v0.2.0...v0.3.0) (2026-08-07)


### ⚠ BREAKING CHANGES

* existing /data/sublime.db files predate the new attempted_providers column. Per ADR 0002's no-migration precedent, upgrading past this release requires deleting /data/sublime.db and accepting a full rescan.

### Features

* add adrs for subdl provider ([5ee9ec9](https://github.com/aaronkyriesenbach/sublime/commit/5ee9ec9ff046aa36e778b22310fbb6ed6f5a7db0))
* add context/adrs ([4caf0f0](https://github.com/aaronkyriesenbach/sublime/commit/4caf0f0dda936e1a15fc2cb93e3d7de093464719))
* add context/adrs ([c0524e3](https://github.com/aaronkyriesenbach/sublime/commit/c0524e37baed1f7bcb08db272b67ccea18925b67))
* add In Progress Sync Status foundation ([#52](https://github.com/aaronkyriesenbach/sublime/issues/52)) ([287f8cb](https://github.com/aaronkyriesenbach/sublime/commit/287f8cbec4f83500505a244ed868a7ab1a78cc92))
* add In Progress transition, marker-gate fast path, uniform status logging ([#54](https://github.com/aaronkyriesenbach/sublime/issues/54)) ([18f75eb](https://github.com/aaronkyriesenbach/sublime/commit/18f75eba66336033da8626b9bc79860454761d8f))
* atomic claim guard for MarkInProgress ([#62](https://github.com/aaronkyriesenbach/sublime/issues/62)) ([e341371](https://github.com/aaronkyriesenbach/sublime/commit/e3413717bd9590e77cb97f47d9e880926c8e3cce))
* docs/adr updates ([6715b65](https://github.com/aaronkyriesenbach/sublime/commit/6715b65b552f1dda1f882bf228ce53eb0a634367))
* implement no-candidate chain-advance and exhaustion ([#76](https://github.com/aaronkyriesenbach/sublime/issues/76)) ([8764c62](https://github.com/aaronkyriesenbach/sublime/commit/8764c622af288865eaa5c717c1b0b0c975098f2e))
* improve observability ([91509aa](https://github.com/aaronkyriesenbach/sublime/commit/91509aa8c7e1005aa9f3addb8284a12fc01f0fa3))
* many improvements ([fb50fe3](https://github.com/aaronkyriesenbach/sublime/commit/fb50fe3033c425ccca669ce254c607fea7fd2ace))
* nested-Tier providers.chain parsing ([#73](https://github.com/aaronkyriesenbach/sublime/issues/73)) ([7afcdaa](https://github.com/aaronkyriesenbach/sublime/commit/7afcdaacfe66e193e51b8b124d701910162d1923))
* OpenSubtitles Provider self-suspends on quota exhaustion ([36a67e0](https://github.com/aaronkyriesenbach/sublime/commit/36a67e0bfc295adeddf6fff25e2c0cbb6a8ea0f6))
* persist per-pair attempted-Provider tracking ([#74](https://github.com/aaronkyriesenbach/sublime/issues/74)) ([45f074e](https://github.com/aaronkyriesenbach/sublime/commit/45f074e76c88a5454b6336c990fbdae9350c2676))
* Provider Chain with per-Provider worker pools [#65](https://github.com/aaronkyriesenbach/sublime/issues/65) ([7c3d61a](https://github.com/aaronkyriesenbach/sublime/commit/7c3d61ab8ec030b88bc32fc57c7c5b88ee99ee6f))
* punctuation trimming ([41bf910](https://github.com/aaronkyriesenbach/sublime/commit/41bf9100131a9cf4e118b6ff3fa7f4aa82a7488f))
* quota-exhausted files reset to Pending, not Failed ([7966c26](https://github.com/aaronkyriesenbach/sublime/commit/7966c269b495a0b8cace973e9e90b05dfc879774)), closes [#57](https://github.com/aaronkyriesenbach/sublime/issues/57)
* show in progress, add error logging ([ddc3035](https://github.com/aaronkyriesenbach/sublime/commit/ddc303599314f4a61c6657938a5ba3b805204a0f))
* split Trigger registration from Dispatcher claim/fetch ([#63](https://github.com/aaronkyriesenbach/sublime/issues/63)) ([0a5039f](https://github.com/aaronkyriesenbach/sublime/commit/0a5039f2a961b4d826ea9583114ac5f69d521af8))
* stream Library scan discovery with Found/Changed logging ([#53](https://github.com/aaronkyriesenbach/sublime/issues/53)) ([aec8c09](https://github.com/aaronkyriesenbach/sublime/commit/aec8c095f054227ac5ddfb7c83828df055300dbf))
* SubDL provider quota exhaustion and Suspension ([#70](https://github.com/aaronkyriesenbach/sublime/issues/70)) ([37640e4](https://github.com/aaronkyriesenbach/sublime/commit/37640e4125d53c1145c7b8ad92b83d0fb396207f))
* SubDL provider search/download happy path ([#69](https://github.com/aaronkyriesenbach/sublime/issues/69)) ([5c8a6b2](https://github.com/aaronkyriesenbach/sublime/commit/5c8a6b2f19a43a3454320530652db25bdba3aa17))
* subdl search unpacks season packs via media.Classify ([#79](https://github.com/aaronkyriesenbach/sublime/issues/79)) ([1bafef7](https://github.com/aaronkyriesenbach/sublime/commit/1bafef71ad79b321864aca2840a304c23f738fff))
* subdl search walks paged results ([#78](https://github.com/aaronkyriesenbach/sublime/issues/78)) ([38b6903](https://github.com/aaronkyriesenbach/sublime/commit/38b6903546e67b74860ca76ccfb427dc3bc7a95c))
* surface Provider Suspended state in GET /status ([#58](https://github.com/aaronkyriesenbach/sublime/issues/58)) ([20d901d](https://github.com/aaronkyriesenbach/sublime/commit/20d901d27bb6c5ce9f1a6ef825438038aa8e27f8))
* tier-scoped Suspension fallback in Dispatcher ([#75](https://github.com/aaronkyriesenbach/sublime/issues/75)) ([31ccf5a](https://github.com/aaronkyriesenbach/sublime/commit/31ccf5aae3381aec278c63ab639fcf83ebe2cf0c))
* update context/adrs for scan/dispatch decoupling ([c9a02bc](https://github.com/aaronkyriesenbach/sublime/commit/c9a02bc54e0990d6264ece4fda7d501c49bb9fd1))
* widen provider config to providers: {chain, &lt;name&gt;} ([#68](https://github.com/aaronkyriesenbach/sublime/issues/68)) ([bcac837](https://github.com/aaronkyriesenbach/sublime/commit/bcac83757fd2d40f3fbbce72d8b06ae0df51901c))
* wire SubDL into production multi-Provider chain ([#71](https://github.com/aaronkyriesenbach/sublime/issues/71)) ([6feca7c](https://github.com/aaronkyriesenbach/sublime/commit/6feca7cfcdef24f0edee168e984071a3c74c3905))


### Bug Fixes

* add lefthook, fix lint issue ([d784a78](https://github.com/aaronkyriesenbach/sublime/commit/d784a78030d44714e0bf5bc8861de2b1d0d6e5be))
* exclude temp files from discovery ([fe2e6fd](https://github.com/aaronkyriesenbach/sublime/commit/fe2e6fd1e463f5190f000009f1b5c34f0d6bdec0))
* formatting ([bd14d14](https://github.com/aaronkyriesenbach/sublime/commit/bd14d14d2b14f599836fd86bc7cabb7f8584df83))
* gate dispatch claims on Provider suspension ([#64](https://github.com/aaronkyriesenbach/sublime/issues/64)) ([35899a3](https://github.com/aaronkyriesenbach/sublime/commit/35899a36b6ec014b7ac444ea9d50cecbd229ddb6))
* remove temp files and rows for deleted files ([8a4eab6](https://github.com/aaronkyriesenbach/sublime/commit/8a4eab60230c633f67d57b19eea3b13cd35f3fa5))
* subdl unzips zip-shape download responses ([051cb39](https://github.com/aaronkyriesenbach/sublime/commit/051cb392cf69c5922f8161653237e966844a5461)), closes [#80](https://github.com/aaronkyriesenbach/sublime/issues/80)
* subdl wire formats ([7c4f505](https://github.com/aaronkyriesenbach/sublime/commit/7c4f505c4aebf0a31a02ec1cf4ae1b52df350daa))


### Miscellaneous Chores

* release 0.3.0 ([cc97b49](https://github.com/aaronkyriesenbach/sublime/commit/cc97b49a45b422ca47e4b4ba101018b2e13da214))

## [0.2.0](https://github.com/aaronkyriesenbach/sublime/compare/v0.1.0...v0.2.0) (2026-08-05)


### Features

* add CLI/HTTP API surface (serve, status, reprocess, libraries) ([9c7704a](https://github.com/aaronkyriesenbach/sublime/commit/9c7704a6f56115b98747693a37614f9d39ab8b01))
* add context ([20a6ad2](https://github.com/aaronkyriesenbach/sublime/commit/20a6ad2e83fa463f21b0ff398ddaa7973b36726e))
* add Marker format & sidecar codec ([#33](https://github.com/aaronkyriesenbach/sublime/issues/33)) ([2eb384d](https://github.com/aaronkyriesenbach/sublime/commit/2eb384df07678f674211a6eef6cfdd7dec0e6c97))
* add pipeline orchestrator for end-to-end subtitle sync ([3812b42](https://github.com/aaronkyriesenbach/sublime/commit/3812b42e549386aa29925268cf369db68cda438e))
* add Provider abstraction and fake Provider ([edb263f](https://github.com/aaronkyriesenbach/sublime/commit/edb263f331fa806ccb1165a91ce9a57c7e4f12d3))
* add retry/backoff executor ([#38](https://github.com/aaronkyriesenbach/sublime/issues/38)) ([910f9ee](https://github.com/aaronkyriesenbach/sublime/commit/910f9eefc99e42af88e8565c5addf9d9665c79b4))
* add SQLite state store ([#31](https://github.com/aaronkyriesenbach/sublime/issues/31)) ([b2866b2](https://github.com/aaronkyriesenbach/sublime/commit/b2866b27dc6e7e65fa62b609698da8a8da389a79))
* add Strip mechanism ([#34](https://github.com/aaronkyriesenbach/sublime/issues/34)) ([82d1e77](https://github.com/aaronkyriesenbach/sublime/commit/82d1e776323ec3f3511de41b0dcf1847eaab07ca))
* add SyncEngine interface + fake sync engine ([b0d590e](https://github.com/aaronkyriesenbach/sublime/commit/b0d590e0784859f5b6ad88607c4d3bf73eec57c0))
* add test data generator script ([c0a24c6](https://github.com/aaronkyriesenbach/sublime/commit/c0a24c672a9f686364cd7e99568ba908812354b9))
* add trigger model (initial scan, fsnotify watch, reprocess) ([5f5aad0](https://github.com/aaronkyriesenbach/sublime/commit/5f5aad06711fbfe5dd2c21bdda78e62047eac090))
* add two-stage Dockerfile and image packaging ([9020e3a](https://github.com/aaronkyriesenbach/sublime/commit/9020e3a0411bebb8a8b0698ca83fd9254cd1fced))
* content hash & content-type detection ([#32](https://github.com/aaronkyriesenbach/sublime/issues/32)) ([81bb4e9](https://github.com/aaronkyriesenbach/sublime/commit/81bb4e9e222ae6a1d485e640ec9e8675ebe7f354))
* create agent docs ([1016d28](https://github.com/aaronkyriesenbach/sublime/commit/1016d28ff4ae8e15acb0d6b78f154b6cae65c2ea))
* real alass Sync Engine ([#43](https://github.com/aaronkyriesenbach/sublime/issues/43)) ([8d1ebd8](https://github.com/aaronkyriesenbach/sublime/commit/8d1ebd8f9550571fa531ef63cfc4e454af6160a7))
* real OpenSubtitles Provider ([#42](https://github.com/aaronkyriesenbach/sublime/issues/42)) ([5081e4b](https://github.com/aaronkyriesenbach/sublime/commit/5081e4bd4e9da58ea0bd84fd1bc3d9c93feedbde))
* scaffold Go module, config loading, libraries CLI ([1017826](https://github.com/aaronkyriesenbach/sublime/commit/1017826a9aeafdffedc2a24188a79139b1f5ea70))
* scoring & matching engine ([#37](https://github.com/aaronkyriesenbach/sublime/issues/37)) ([fd28ce7](https://github.com/aaronkyriesenbach/sublime/commit/fd28ce76a75e5f8374211812089a5888391c5d85))
* update context, fix tv parse bugs ([f7a8d87](https://github.com/aaronkyriesenbach/sublime/commit/f7a8d879bfe97ab178c788c7d2bc07a407a6a18f))
* wire real Provider & Sync Engine into daemon ([#44](https://github.com/aaronkyriesenbach/sublime/issues/44)) ([000bca7](https://github.com/aaronkyriesenbach/sublime/commit/000bca753e52134799990f4402084e5aaeb2f2a3))


### Bug Fixes

* compute Content Hash after Strip's embedded-stream removal ([61d6a05](https://github.com/aaronkyriesenbach/sublime/commit/61d6a05c6ef9ce2d40dab831e612dfbcf640498f))
