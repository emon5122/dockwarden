# Changelog

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
