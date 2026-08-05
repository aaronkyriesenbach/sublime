# Changelog

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
