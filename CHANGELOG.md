# Changelog

## [1.2.2](https://github.com/emon5122/dockwarden/compare/v1.2.1...v1.2.2) (2026-08-05)


### Bug Fixes

* watcher has better logic ([acdc95a](https://github.com/emon5122/dockwarden/commit/acdc95ad24516138fa917c196325dd77185cfba2))

## [1.2.1](https://github.com/emon5122/dockwarden/compare/v1.2.0...v1.2.1) (2026-06-19)


### Bug Fixes

* Refactor image environment and label handling in container recreation ([1a84fc6](https://github.com/emon5122/dockwarden/commit/1a84fc6f8509c4d92e0295263e627841ea139400))

## [1.2.0](https://github.com/emon5122/dockwarden/compare/v1.1.3...v1.2.0) (2026-06-13)


### Features

* Add health monitoring configuration and enhance watcher logic ([4ea7353](https://github.com/emon5122/dockwarden/commit/4ea73533a788664a48219d1033d455ef07dcba21))
* Enhance concurrency handling in health checks and updates, improve error handling in metrics ([e062ad6](https://github.com/emon5122/dockwarden/commit/e062ad6f427815fc02c0551ab2343b30b20cc4e5))


### Bug Fixes

* **deps:** update module github.com/moby/moby/client to v0.4.1 ([1e46a2a](https://github.com/emon5122/dockwarden/commit/1e46a2ae403d8c6e90017d97cc884437b1e12af4))

## [1.1.3](https://github.com/emon5122/dockwarden/compare/v1.1.2...v1.1.3) (2026-05-06)


### Bug Fixes

* update take-over command to force-remove old containers and clean up stale instances ([608cd98](https://github.com/emon5122/dockwarden/commit/608cd985ad53484d205d5c7adc08ed6e5faec802))

## [1.1.2](https://github.com/emon5122/dockwarden/compare/v1.1.1...v1.1.2) (2026-05-06)


### Bug Fixes

* add tests for stripInheritedImageEnv function ([7356011](https://github.com/emon5122/dockwarden/commit/7356011359cb532edbb2fa407ca2606564d67135))

## [1.1.1](https://github.com/emon5122/dockwarden/compare/v1.1.0...v1.1.1) (2026-04-06)


### Bug Fixes

* **deps:** update module github.com/gin-gonic/gin to v1.12.0 ([c74bc00](https://github.com/emon5122/dockwarden/commit/c74bc00b2a8ab99624512cc58dfd0d3bb1cd3b36))
* **deps:** update module github.com/gin-gonic/gin to v1.12.0 ([980d435](https://github.com/emon5122/dockwarden/commit/980d4351edf587b2eac34bf9f12b66706b3377a8))
* **deps:** update module github.com/sirupsen/logrus to v1.9.4 ([8846cd4](https://github.com/emon5122/dockwarden/commit/8846cd491e553ddc31f51d3645d560694a4c3679))
* **deps:** update module github.com/sirupsen/logrus to v1.9.4 ([8aa1425](https://github.com/emon5122/dockwarden/commit/8aa14256b3d84143b20fe0f552d568b229cd0d39))
* **deps:** update module github.com/spf13/viper to v1.21.0 ([a19b89c](https://github.com/emon5122/dockwarden/commit/a19b89c2e76773b215cb9b81865b0e29d96aafd0))
* **deps:** update module github.com/spf13/viper to v1.21.0 ([88f1c9f](https://github.com/emon5122/dockwarden/commit/88f1c9fc20e66d4f0246e559e09bc257fb72ab87))

## [1.1.0](https://github.com/emon5122/dockwarden/compare/v1.0.7...v1.1.0) (2026-04-06)


### Features

* Implement session-based authentication and login/logout functionality ([61c5ea0](https://github.com/emon5122/dockwarden/commit/61c5ea0f3f10e7819a8575c80dcb2642ebd76178))


### Bug Fixes

* update Go version to 1.25.8 in CI workflow ([d6829c6](https://github.com/emon5122/dockwarden/commit/d6829c6d902675b174335bec6da0aaeb1ca5a669))

## [1.0.7](https://github.com/emon5122/dockwarden/compare/v1.0.6...v1.0.7) (2026-02-01)


### Bug Fixes

* prevent stale DNS entries by allowing Docker to assign new MAC addresses in RecreateContainer ([94d4466](https://github.com/emon5122/dockwarden/commit/94d4466cd0dab98f6bf602196c278bcc0931540c))

## [1.0.6](https://github.com/emon5122/dockwarden/compare/v1.0.5...v1.0.6) (2026-01-31)


### Bug Fixes

* update Go version to 1.25.6 in Dockerfile, go.mod, and workflows ([9e3dc83](https://github.com/emon5122/dockwarden/commit/9e3dc8321c3f32d6b16d39921ff5776eaa13f2aa))

## [1.0.5](https://github.com/emon5122/dockwarden/compare/v1.0.4...v1.0.5) (2026-01-31)


### Bug Fixes

* solved some high CVEs through deps ([3c19557](https://github.com/emon5122/dockwarden/commit/3c19557635a166e2ef43f857540e2cf63cdee089))

## [1.0.4](https://github.com/emon5122/dockwarden/compare/v1.0.3...v1.0.4) (2026-01-31)


### Bug Fixes

* enhance RecreateContainer to preserve and reconnect network settings ([26c3bd4](https://github.com/emon5122/dockwarden/commit/26c3bd4b5666891a9908e490ff2755ff1050a841))

## [1.0.3](https://github.com/emon5122/dockwarden/compare/v1.0.2...v1.0.3) (2026-01-31)


### Bug Fixes

* update GoReleaser configuration and improve logging levels ([299a9a0](https://github.com/emon5122/dockwarden/commit/299a9a038cd4124df6664e3563df5142a4151473))

## [1.0.2](https://github.com/emon5122/dockwarden/compare/v1.0.1...v1.0.2) (2026-01-31)


### Bug Fixes

* Implement self-update protection for dockwarden container and enhance Docker auth handling ([537770d](https://github.com/emon5122/dockwarden/commit/537770deaea6197ba73cc9d2e79b1f0a67275c8b))
* Update GoReleaser action to v6 and specify version constraint ([68603dd](https://github.com/emon5122/dockwarden/commit/68603dd635b50b991b60726fb4d3885c878e5673))

## [1.0.1](https://github.com/emon5122/dockwarden/compare/v1.0.0...v1.0.1) (2026-01-31)


### Bug Fixes

* Update Go version to 1.24 in workflow and add .goreleaser.yaml for release management ([550e9dd](https://github.com/emon5122/dockwarden/commit/550e9dd66f389b676c26fc40f98d65e213756f0a))

## 1.0.0 (2026-01-31)


### Features

* Implement notification system for container updates, health checks, and restarts ([35f5acf](https://github.com/emon5122/dockwarden/commit/35f5acf7fcfaf4b3fc1e63f08b25f693edd02d64))


### Bug Fixes

* Update Docker login credentials and permissions in workflows ([a4dc33c](https://github.com/emon5122/dockwarden/commit/a4dc33c5cd13d7063812e775773898b4fde108a3))
