// Package broker owns the shape of the embedded NATS server that `loc start`
// runs.
//
// IT EXISTS SO THE TESTS CANNOT BOOT A DIFFERENT SERVER FROM THE PRODUCT. The
// options were spelled inline in the daemon, and the integration harness spelled
// its own set beside them; a provider suite that passed against the harness's
// server proved nothing about the one the daemon boots. One builder, two
// callers: cmd/loc/daemon.go and internal/loctest.
//
// It is its own package because internal/loctest cannot import cmd/loc, which
// is a main package.
package broker

import (
	natsserver "github.com/nats-io/nats-server/v2/server"
)

// Options is the server `loc start` runs: JetStream on, its store under the
// deployment's home, no logging and no signal handling of the library's own.
//
// THERE IS NO AUTHORIZATION BLOCK. V0 puts no authentication on the loopback
// listener (R12, 2026-09-09). The listener's address is the whole of the
// posture, and `loc start` refuses a non-loopback one (R35a).
//
// port -1 asks the kernel for an ephemeral one, which is what a test wants and
// what a deployment never asks for.
func Options(host string, port int, storeDir string) *natsserver.Options {
	return &natsserver.Options{
		Host:      host,
		Port:      port,
		JetStream: true,
		StoreDir:  storeDir,
		NoLog:     true,
		// The endings belong to the caller. The library's own handler would
		// exit before the daemon removes its pidfile.
		NoSigs: true,
	}
}
