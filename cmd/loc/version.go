package main

// buildVersion is stamped at link time. `make build` passes the value:
//
//	go build -ldflags "-X main.buildVersion=$(git describe --tags --dirty --always | sed 's/^v//')"
//
// A plain `go build` leaves it empty.
var buildVersion = ""

// version returns the one number this build claims to be.
//
// The number is `git describe`, stamped at link time. On a tagged commit it is
// bare. Off a tag it carries `-N-g<sha>`, which names the commit. A dirty tree
// adds `-dirty`. A plain `go build` stamps nothing, and such a binary prints
// `dev`: it has no number to claim, and it must not invent one.
//
// The error result stays because the two call sites read it. Nothing here can
// fail any more, so it is always nil.
func version() (string, error) {
	if buildVersion != "" {
		return buildVersion, nil
	}
	return "dev", nil
}
