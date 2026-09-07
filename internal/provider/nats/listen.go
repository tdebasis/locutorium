package nats

import (
	"fmt"
	"strconv"
	"time"

	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/provider"
)

// This file is the LISTENER's half of the adapter: how a seat is told that
// mail has arrived, without taking any of it.
var _ provider.Listener = (*Provider)(nil)

// listenReconnectWait is how long the listener's connection waits between
// attempts to re-establish itself. A seat is registered for the whole of a
// session, so it retries for as long as it is running.
const listenReconnectWait = 1 * time.Second

// WatchQueue holds a CORE subscription on the endpoint's own queue subject.
//
// CORE, NOT A CONSUMER. The stream captures the same publish, so this sees
// every arrival while consuming nothing: the mail stays in the queue for the
// read that comes after the bell, and the listener never competes with the
// reader for it. It is also why nothing here needs a cursor, an
// acknowledgement, or any state that could be lost.
//
// The connection is the ONE this package makes that is meant to survive a
// broker restart. Every other is a CLI process doing one thing and leaving,
// where a reconnect would only turn a dead server into a long wait; a listener
// that gave up on the first blip would instead leave its seat registered and
// deaf, which is the failure nobody notices. Each re-establishment calls
// reconnected, because arrivals during the gap were seen by no one.
func (p *Provider) WatchQueue(endpoint string, arrived, reconnected func()) (func(), error) {
	id, pass, url, err := credentials()
	if err != nil {
		return nil, err
	}
	nc, err := natsgo.Connect(url,
		natsgo.UserInfo(id, pass),
		natsgo.Name("loc"),
		natsgo.Timeout(ackTimeout),
		natsgo.ErrorHandler(p.noteRefusal),
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectWait(listenReconnectWait),
		natsgo.ReconnectHandler(func(*natsgo.Conn) {
			if reconnected != nil {
				reconnected()
			}
		}),
	)
	if err != nil {
		return nil, errConnect
	}
	sub, err := nc.Subscribe("queue."+endpoint, func(*natsgo.Msg) {
		if arrived != nil {
			arrived()
		}
	})
	if err == nil {
		// Flushed before returning, so that a caller which asks for the
		// backlog immediately afterwards cannot miss an arrival in the gap
		// between the subscription being made and it being registered at the
		// server.
		err = nc.Flush()
	}
	if err != nil {
		// Closing the connection takes the subscription with it; there is no
		// half-established watch to leave behind.
		nc.Close()
		return nil, err
	}
	return func() { _ = sub.Unsubscribe(); nc.Close() }, nil
}

// Unread is how many messages an endpoint has not taken, as a number.
//
// It is the FIGURE `status` PRINTS, asked for by the one caller that has to do
// arithmetic with it. status prints "?" when the number cannot be had, because
// a report that refuses to print because one number is missing is no report;
// a caller deciding whether to ring a bell needs the difference between "none"
// and "cannot say", so here it is an error.
func (p *Provider) Unread(endpoint string) (int, error) {
	if err := p.connect(); err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(p.unread(endpoint))
	if err != nil {
		return 0, fmt.Errorf("the store cannot say how much mail '%s' has not taken", endpoint)
	}
	return n, nil
}
