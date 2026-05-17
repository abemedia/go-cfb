# Changelog

## [0.1.0](https://github.com/abemedia/go-cfb/compare/v0.0.1...v0.1.0) (2026-05-16)


### Features

* implement MS-CFB reader & writer ([#1](https://github.com/abemedia/go-cfb/issues/1)) ([cb8bd80](https://github.com/abemedia/go-cfb/commit/cb8bd802d9a0090d1e3dcbf7b9ca0d3054a19f0e))


### Bug Fixes

* reject headers declaring more sectors than the file can hold ([#10](https://github.com/abemedia/go-cfb/issues/10)) ([df261b2](https://github.com/abemedia/go-cfb/commit/df261b20565254e6d9559d5433d103e28f5771a3))


### Performance Improvements

* compare entry names without heap allocation ([#13](https://github.com/abemedia/go-cfb/issues/13)) ([5c01f34](https://github.com/abemedia/go-cfb/commit/5c01f3453864f2bfa201f9f933a5b18d47ee4c6b))
