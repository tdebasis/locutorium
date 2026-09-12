# Releasing

CI tags every merge to `main` and moves the third digit by one.

A first-digit or second-digit move is the maintainer's hand tag. CI carries on from the highest tag
it finds, so a hand tag is honoured.

The binary prints `git describe`. On a tagged commit the number is bare. On any other commit it
carries `-N-g<sha>`. An unclean tree adds `-dirty`. A plain `go build` prints `dev`.

Under `0.x` the third digit is a merge counter. It promises nothing about what changed.

To install, run `git fetch --tags` on `main`, then `./install.sh`.
