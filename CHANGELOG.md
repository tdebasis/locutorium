# Changelog

## [0.0.11](https://github.com/tdebasis/locutorium/compare/v0.0.10...v0.0.11) (2026-09-20)


### Bug Fixes

* **check-clean:** scan the commits a pull request adds ([#182](https://github.com/tdebasis/locutorium/issues/182)) ([1c761ed](https://github.com/tdebasis/locutorium/commit/1c761edf01e5e9876b81eb4748529ca1c364f65a))

## [0.0.10](https://github.com/tdebasis/locutorium/compare/v0.0.9...v0.0.10) (2026-09-20)


### Features

* **bell:** try three times while mail is unread, then give up ([#171](https://github.com/tdebasis/locutorium/issues/171)) ([8e0513b](https://github.com/tdebasis/locutorium/commit/8e0513bd7dda9dfa3339f44fa31513c10e640716))
* **registry:** list every registered agent from the ledger ([#167](https://github.com/tdebasis/locutorium/issues/167)) ([0db91f5](https://github.com/tdebasis/locutorium/commit/0db91f5f2b36efaec832b284b6a3db5ab5f84ad7))
* **status:** report a queue whose name the sweep cannot read ([#168](https://github.com/tdebasis/locutorium/issues/168)) ([7a897f3](https://github.com/tdebasis/locutorium/commit/7a897f313cf95903877a603ad6e5262b0791ff07))


### Bug Fixes

* **sweep:** report a queue with no row and never delete it ([#170](https://github.com/tdebasis/locutorium/issues/170)) ([2f6faef](https://github.com/tdebasis/locutorium/commit/2f6faef25e338223fcbd4db01bed12bc8ad104a4))

## [0.0.9](https://github.com/tdebasis/locutorium/compare/v0.0.8...v0.0.9) (2026-09-19)


### Features

* **status:** say whether a seat is attending ([#160](https://github.com/tdebasis/locutorium/issues/160)) ([2eaa9ef](https://github.com/tdebasis/locutorium/commit/2eaa9ef41bf2aab59e5508cab941c687c3a4a185))


### Bug Fixes

* **status:** count unread mail as the queue depth ([#164](https://github.com/tdebasis/locutorium/issues/164)) ([5928135](https://github.com/tdebasis/locutorium/commit/5928135e2915b8cdabb194618a53277d5f133bb2))

## [0.0.8](https://github.com/tdebasis/locutorium/compare/v0.0.7...v0.0.8) (2026-09-19)


### Bug Fixes

* **conformance:** apply the generic list together with the deployment list ([#157](https://github.com/tdebasis/locutorium/issues/157)) ([1ca330c](https://github.com/tdebasis/locutorium/commit/1ca330ca65b5bff8d114c009065f5e37bef22a31))

## [0.0.7](https://github.com/tdebasis/locutorium/compare/v0.0.6...v0.0.7) (2026-09-19)


### Features

* **cli:** add the unread verb, a count that a status line can trust ([#153](https://github.com/tdebasis/locutorium/issues/153)) ([4f80617](https://github.com/tdebasis/locutorium/commit/4f806173d5c6b57910ff4c01d5bc205ecb3af3c3))


### Bug Fixes

* **bell:** make a ring that the breaker suppressed later ([#152](https://github.com/tdebasis/locutorium/issues/152)) ([7f61a5d](https://github.com/tdebasis/locutorium/commit/7f61a5d93b7847e52d0e17dd591b8964be3d8124))
* **bell:** ring again after a busy pane refuses ([#150](https://github.com/tdebasis/locutorium/issues/150)) ([2748f63](https://github.com/tdebasis/locutorium/commit/2748f6314bfdb92fa89141d5f9f0347e2e7df2fa))
* **conformance:** isolate the tag-mode fixture from the real tags ([#128](https://github.com/tdebasis/locutorium/issues/128)) ([2f1a36b](https://github.com/tdebasis/locutorium/commit/2f1a36b74530ee2c8ff9ff004e955c991c6a7cd3))
* **daemon:** refuse a home with no config file, and sweep only the daemon's own broker ([#147](https://github.com/tdebasis/locutorium/issues/147)) ([4b765d5](https://github.com/tdebasis/locutorium/commit/4b765d5558aeea20ad0ac754cc54a31a5c87889f))
* **presence:** limit the sweep to instances in the ledger ([#140](https://github.com/tdebasis/locutorium/issues/140)) ([5f9fc0e](https://github.com/tdebasis/locutorium/commit/5f9fc0e2ec82a43f5dfb4c45e0508d69c61fb67c))

## [0.0.6](https://github.com/tdebasis/locutorium/compare/v0.0.5...v0.0.6) (2026-09-18)


### Features

* **bell:** name the wake courier after the sender ([#125](https://github.com/tdebasis/locutorium/issues/125)) ([57dfc74](https://github.com/tdebasis/locutorium/commit/57dfc748d60f28235d9f4af4ae9f645ea14c0d28))
* **config:** rename idle_window to idle_timeout ([#117](https://github.com/tdebasis/locutorium/issues/117)) ([00ad5c1](https://github.com/tdebasis/locutorium/commit/00ad5c1ceac8f75ba003d353f2b637d087e3c569))


### Bug Fixes

* **loc:** split the pid liveness probe by platform ([#119](https://github.com/tdebasis/locutorium/issues/119)) ([5cc2ab2](https://github.com/tdebasis/locutorium/commit/5cc2ab274100c503867a0ef59631a1d29422771b))
* **read:** release the in-flight message when a signal kills the read ([#121](https://github.com/tdebasis/locutorium/issues/121)) ([b551f76](https://github.com/tdebasis/locutorium/commit/b551f7663eb6e1d59d3c22eb9db3a6e06ec44def))

## [0.0.5](https://github.com/tdebasis/locutorium/compare/v0.0.4...v0.0.5) (2026-09-17)


### Bug Fixes

* **cli:** use the repository's fixture in a comment example ([#114](https://github.com/tdebasis/locutorium/issues/114)) ([58285af](https://github.com/tdebasis/locutorium/commit/58285af60f2a1e0788d97e67452e8f3c61faf798))

## [0.0.4](https://github.com/tdebasis/locutorium/compare/v0.0.3...v0.0.4) (2026-09-17)


### Features

* **cli:** add loc transcript to read the message log back ([#112](https://github.com/tdebasis/locutorium/issues/112)) ([414d954](https://github.com/tdebasis/locutorium/commit/414d9546362a5af3c652ca3fb2ca03057b47e844))
* **status:** report a failed bell from the message log ([#110](https://github.com/tdebasis/locutorium/issues/110)) ([f7fc43e](https://github.com/tdebasis/locutorium/commit/f7fc43ee0ef66fcea2ea5c3cb37f1229fc7566da))

## [0.0.3](https://github.com/tdebasis/locutorium/compare/v0.0.2...v0.0.3) (2026-09-17)


### Features

* **log:** release the event log and the test guard ([#108](https://github.com/tdebasis/locutorium/issues/108)) ([c435933](https://github.com/tdebasis/locutorium/commit/c4359335d3a53ef1709826a7fc6ee7ccf73baba7))

## [0.0.2](https://github.com/tdebasis/locutorium/compare/v0.0.1...v0.0.2) (2026-09-16)


### Bug Fixes

* **conformance:** skip a worktree pointer in the clean check, and install from a tag in the suite ([#98](https://github.com/tdebasis/locutorium/issues/98)) ([af31c08](https://github.com/tdebasis/locutorium/commit/af31c0829d83b410f0616247aea18f256436c6de))

## 0.0.1 (2026-09-16)


### Features

* **install:** build the newest release tag by default ([#95](https://github.com/tdebasis/locutorium/issues/95)) ([88d991b](https://github.com/tdebasis/locutorium/commit/88d991b9a1fb0c7d886c7493a2624791ce0ad361))
