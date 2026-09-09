# Changelog

## [0.3.0](https://github.com/aaronkyriesenbach/sublime/compare/v0.2.0...v0.3.0) (2026-09-09)


### ⚠ BREAKING CHANGES

* existing /data/sublime.db files predate the new attempted_providers column. Per ADR 0002's no-migration precedent, upgrading past this release requires deleting /data/sublime.db and accepting a full rescan.

### Features

* add adrs for subdl provider ([b708d7c](https://github.com/aaronkyriesenbach/sublime/commit/b708d7ce7bac32d3577c6d86674bc7404c6eeb3f))
* add CLI/HTTP API surface (serve, status, reprocess, libraries) ([487e9ec](https://github.com/aaronkyriesenbach/sublime/commit/487e9ec95d84d607f02482085d76ecb154ea47e7))
* add context ([f27bbcd](https://github.com/aaronkyriesenbach/sublime/commit/f27bbcd9b645c7d50db41066db92cc3baa1c8d82))
* add context/adrs ([7de98fb](https://github.com/aaronkyriesenbach/sublime/commit/7de98fb8b7babb5849d1f4d40f834504387c141d))
* add context/adrs ([0ac38a5](https://github.com/aaronkyriesenbach/sublime/commit/0ac38a54f9380f8d7328027c0448987dee384e5c))
* add In Progress Sync Status foundation ([#52](https://github.com/aaronkyriesenbach/sublime/issues/52)) ([34067b3](https://github.com/aaronkyriesenbach/sublime/commit/34067b31fa4cb64506a8478697c00d3262414eb0))
* add In Progress transition, marker-gate fast path, uniform status logging ([#54](https://github.com/aaronkyriesenbach/sublime/issues/54)) ([8817fd7](https://github.com/aaronkyriesenbach/sublime/commit/8817fd77f8bde481d854e60861f4d5f4622e067c))
* add Marker format & sidecar codec ([#33](https://github.com/aaronkyriesenbach/sublime/issues/33)) ([7fa703c](https://github.com/aaronkyriesenbach/sublime/commit/7fa703c250f3807c12e1a0ea75e2c9a60ddcb8ab))
* add pipeline orchestrator for end-to-end subtitle sync ([102a74c](https://github.com/aaronkyriesenbach/sublime/commit/102a74ce9e5a591989434a3794fb60de154fae6e))
* add Provider abstraction and fake Provider ([cc038d3](https://github.com/aaronkyriesenbach/sublime/commit/cc038d3367ce157f9cb617d282388a580387bf80))
* add retry/backoff executor ([#38](https://github.com/aaronkyriesenbach/sublime/issues/38)) ([e013c7a](https://github.com/aaronkyriesenbach/sublime/commit/e013c7a894e8a9e897067121b32f9fb5a14faa2c))
* add SQLite state store ([#31](https://github.com/aaronkyriesenbach/sublime/issues/31)) ([7186887](https://github.com/aaronkyriesenbach/sublime/commit/7186887e2d4b4e7633f52ad17245586e1076f73f))
* add Strip mechanism ([#34](https://github.com/aaronkyriesenbach/sublime/issues/34)) ([570e82d](https://github.com/aaronkyriesenbach/sublime/commit/570e82d7c7e9fb4e29fbe3acfd368424b41d3bef))
* add SyncEngine interface + fake sync engine ([3a94af7](https://github.com/aaronkyriesenbach/sublime/commit/3a94af7e288cc4927c407d371840ca180aeed8e2))
* add test data generator script ([284cd4e](https://github.com/aaronkyriesenbach/sublime/commit/284cd4e85a7f361f238d53e10b03f0316b7fc3b2))
* add trigger model (initial scan, fsnotify watch, reprocess) ([e42d993](https://github.com/aaronkyriesenbach/sublime/commit/e42d99354e8ea7f1d794bed244bf56b7fa0a7743))
* add two-stage Dockerfile and image packaging ([7b78edd](https://github.com/aaronkyriesenbach/sublime/commit/7b78edd45f5e371856f3e751236490a0b5202d20))
* atomic claim guard for MarkInProgress ([#62](https://github.com/aaronkyriesenbach/sublime/issues/62)) ([799937b](https://github.com/aaronkyriesenbach/sublime/commit/799937bdfdbd567ac327501cab9839f29025ea6c))
* content hash & content-type detection ([#32](https://github.com/aaronkyriesenbach/sublime/issues/32)) ([80fdd19](https://github.com/aaronkyriesenbach/sublime/commit/80fdd19177b7fcb5372f3503b49d7e9d2d6f5de9))
* create agent docs ([7d22694](https://github.com/aaronkyriesenbach/sublime/commit/7d2269445ae2ee3fb0696d145120fd7b8d198a71))
* docs/adr updates ([ab64100](https://github.com/aaronkyriesenbach/sublime/commit/ab6410028d4e750fad16880eb4eac0c6c6385391))
* implement no-candidate chain-advance and exhaustion ([#76](https://github.com/aaronkyriesenbach/sublime/issues/76)) ([381a55c](https://github.com/aaronkyriesenbach/sublime/commit/381a55c0ad24c48b9c247e30bb315fb730c5efb9))
* improve observability ([f2a8037](https://github.com/aaronkyriesenbach/sublime/commit/f2a80379ecd7f6fae71b0a14f2dc26cd1a20a771))
* many improvements ([8f041d0](https://github.com/aaronkyriesenbach/sublime/commit/8f041d04fd6e5cbd1cd4770f3cbb49d0b1a5d3e6))
* nested-Tier providers.chain parsing ([#73](https://github.com/aaronkyriesenbach/sublime/issues/73)) ([3a20a45](https://github.com/aaronkyriesenbach/sublime/commit/3a20a45e7c092c2a63eb7b484160e68f1dbcd7eb))
* OpenSubtitles Provider self-suspends on quota exhaustion ([f4b9986](https://github.com/aaronkyriesenbach/sublime/commit/f4b998687480aef688b4f3b9dd2edd41cca6164f))
* persist per-pair attempted-Provider tracking ([#74](https://github.com/aaronkyriesenbach/sublime/issues/74)) ([511f832](https://github.com/aaronkyriesenbach/sublime/commit/511f8322dda0d8d404b8e714ca7793e7ffaa09e4))
* Provider Chain with per-Provider worker pools [#65](https://github.com/aaronkyriesenbach/sublime/issues/65) ([215ccd6](https://github.com/aaronkyriesenbach/sublime/commit/215ccd684b6f5416a14d524cc23c9ff7ebb79fc8))
* punctuation trimming ([e193c78](https://github.com/aaronkyriesenbach/sublime/commit/e193c78c672caed5d9ddf7ebf4c385234ac9f402))
* quota-exhausted files reset to Pending, not Failed ([09e35fa](https://github.com/aaronkyriesenbach/sublime/commit/09e35fac13242b6b33fb9400c1d3b82b9a6fe0bb)), closes [#57](https://github.com/aaronkyriesenbach/sublime/issues/57)
* real alass Sync Engine ([#43](https://github.com/aaronkyriesenbach/sublime/issues/43)) ([e49e08d](https://github.com/aaronkyriesenbach/sublime/commit/e49e08d631165cbaca31cd38d2a364acf56793c7))
* real OpenSubtitles Provider ([#42](https://github.com/aaronkyriesenbach/sublime/issues/42)) ([e2a8dc5](https://github.com/aaronkyriesenbach/sublime/commit/e2a8dc50527e860369d8748548c71d4ce30b913e))
* scaffold Go module, config loading, libraries CLI ([695871a](https://github.com/aaronkyriesenbach/sublime/commit/695871a85f83210a0805967a39504958b1bcac5b))
* scoring & matching engine ([#37](https://github.com/aaronkyriesenbach/sublime/issues/37)) ([fd2c30c](https://github.com/aaronkyriesenbach/sublime/commit/fd2c30c492c5a0992102f8830f637bd068557be1))
* show in progress, add error logging ([d1dc6bf](https://github.com/aaronkyriesenbach/sublime/commit/d1dc6bfc211c0e237262825125425dc030f3843a))
* split Trigger registration from Dispatcher claim/fetch ([#63](https://github.com/aaronkyriesenbach/sublime/issues/63)) ([d0d4d5e](https://github.com/aaronkyriesenbach/sublime/commit/d0d4d5eedfdba2c4f8fb3c807303bbf1940753cd))
* stream Library scan discovery with Found/Changed logging ([#53](https://github.com/aaronkyriesenbach/sublime/issues/53)) ([8b0c9b3](https://github.com/aaronkyriesenbach/sublime/commit/8b0c9b3b8c26a3f788d9afdab971b9683ccd10c3))
* SubDL provider quota exhaustion and Suspension ([#70](https://github.com/aaronkyriesenbach/sublime/issues/70)) ([101ded2](https://github.com/aaronkyriesenbach/sublime/commit/101ded2b6c9e2288a5503ce15e9d4ac23189489c))
* SubDL provider search/download happy path ([#69](https://github.com/aaronkyriesenbach/sublime/issues/69)) ([02b9cb7](https://github.com/aaronkyriesenbach/sublime/commit/02b9cb77c5677bf7acd5add24aba814f17242b4e))
* subdl search unpacks season packs via media.Classify ([#79](https://github.com/aaronkyriesenbach/sublime/issues/79)) ([4a05b1b](https://github.com/aaronkyriesenbach/sublime/commit/4a05b1bc91dbef074e279aa25d376f8f431ef4e4))
* subdl search walks paged results ([#78](https://github.com/aaronkyriesenbach/sublime/issues/78)) ([adc04c9](https://github.com/aaronkyriesenbach/sublime/commit/adc04c9512d98c3ce3f5c957baacb629e41134fa))
* surface Provider Suspended state in GET /status ([#58](https://github.com/aaronkyriesenbach/sublime/issues/58)) ([ef00c82](https://github.com/aaronkyriesenbach/sublime/commit/ef00c82b0f32466e637c6b9d04365da63a0697a6))
* tier-scoped Suspension fallback in Dispatcher ([#75](https://github.com/aaronkyriesenbach/sublime/issues/75)) ([9c7bd66](https://github.com/aaronkyriesenbach/sublime/commit/9c7bd6641587cad09f72aa0c9533cb7cd7174ca5))
* update context ([4ff1c29](https://github.com/aaronkyriesenbach/sublime/commit/4ff1c29a441b0bbcf0d675a90f65b7e0304c99ea))
* update context, fix tv parse bugs ([a774d34](https://github.com/aaronkyriesenbach/sublime/commit/a774d3403211f773c5db940485019b436cece462))
* update context/adrs for scan/dispatch decoupling ([be11b84](https://github.com/aaronkyriesenbach/sublime/commit/be11b84bf55f5edcf2792c2592216e2e51865413))
* update context/docs ([8b9fc1b](https://github.com/aaronkyriesenbach/sublime/commit/8b9fc1b9c519a7e192e5e6b8fde8ab6ced266030))
* widen provider config to providers: {chain, &lt;name&gt;} ([#68](https://github.com/aaronkyriesenbach/sublime/issues/68)) ([d9f9060](https://github.com/aaronkyriesenbach/sublime/commit/d9f9060da7f29004aa5aa14f72c8723c9b536b7c))
* wire real Provider & Sync Engine into daemon ([#44](https://github.com/aaronkyriesenbach/sublime/issues/44)) ([6949ca7](https://github.com/aaronkyriesenbach/sublime/commit/6949ca7499896c83c89fc780ff86db899d747500))
* wire SubDL into production multi-Provider chain ([#71](https://github.com/aaronkyriesenbach/sublime/issues/71)) ([198a760](https://github.com/aaronkyriesenbach/sublime/commit/198a76009a1e791f813809bd8d4168d4d0ef40af))


### Bug Fixes

* add lefthook, fix lint issue ([1b94cd0](https://github.com/aaronkyriesenbach/sublime/commit/1b94cd00f8d010df2a71e8e91a8715a3fa5be8c4))
* compute Content Hash after Strip's embedded-stream removal ([a191c3c](https://github.com/aaronkyriesenbach/sublime/commit/a191c3c090d51d3de0c62962e83463e19de6552b))
* exclude temp files from discovery ([3f6d751](https://github.com/aaronkyriesenbach/sublime/commit/3f6d751bc8bb228d7ecbe526443e88d761672f43))
* formatting ([4b0e1c4](https://github.com/aaronkyriesenbach/sublime/commit/4b0e1c4fec93a95bbd4bfaabbc0e9f668b0b0a43))
* gate dispatch claims on Provider suspension ([#64](https://github.com/aaronkyriesenbach/sublime/issues/64)) ([a5d9db4](https://github.com/aaronkyriesenbach/sublime/commit/a5d9db4ee9922603071bf89ccd094f40bee2fe4e))
* remove temp files and rows for deleted files ([bdd2394](https://github.com/aaronkyriesenbach/sublime/commit/bdd2394ea65a6a41b0c5e08df6acc6120f0defef))
* subdl unzips zip-shape download responses ([4b574a3](https://github.com/aaronkyriesenbach/sublime/commit/4b574a30b3d3f54466f40e71ea894d1df7b25101)), closes [#80](https://github.com/aaronkyriesenbach/sublime/issues/80)
* subdl wire formats ([fa4c6e1](https://github.com/aaronkyriesenbach/sublime/commit/fa4c6e1ec732f6a1863085279dbc6ab3cb095a53))


### Miscellaneous Chores

* release 0.3.0 ([1278710](https://github.com/aaronkyriesenbach/sublime/commit/1278710eda4f037922d1802b6cd6b12b68bdc5c8))

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
