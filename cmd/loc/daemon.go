package main

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"

	natsgo "github.com/nats-io/nats.go"

	"github.com/tdebasis/locutorium/internal/broker"
	"github.com/tdebasis/locutorium/internal/provider/nats"

	"github.com/tdebasis/locutorium/internal/config"
	model "github.com/tdebasis/locutorium/internal/presence"
)

// beatInterval is the heartbeat. Five minutes is a product guarantee and the
// documents say the number, so it is spelled once here.
const beatInterval = 5 * time.Minute

// readyWait bounds the broker's boot, the parent's poll and the stop's wait
// alike. It is a variable so a test can shorten the wait it asserts on; the
// product uses the ten seconds spelled here.
var readyWait = 10 * time.Second

// listenAddr is where the broker listens, read from `nats_url`.
//
// V0 IS ONE WORKSTATION AND THE LISTENER IS LOOPBACK. A broker with no
// authentication on a routable address hands every queue in the deployment to
// the network. The refusal is here, in the one function both `loc start` and
// the daemon ask, so neither can boot an address the other would have refused.
func listenAddr() (host string, port int, err error) {
	// A HOME WITH NO CONFIG FILE IS NOT A DEPLOYMENT. nats_url would resolve
	// to the table's default, which is the address a real deployment on this
	// machine listens on, and this function is what decides where a broker
	// binds. `loc stop`, `loc stop --force` and a hand-typed
	// `loc start --serve` all arrive here with no file when a home is gone.
	//
	// BARE `loc start` STILL WORKS. startVerb calls surfaceConfig first, which
	// writes the default file, so the file is there by the time this runs.
	// That is also the recovery path for a deployment whose file was deleted.
	if err := config.RequireFile(); err != nil {
		return "", 0, err
	}
	raw := config.Value(config.NATSURL)
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0, fmt.Errorf("cannot read %s '%s': %v", config.NATSURL, raw, err)
	}
	host = u.Hostname()
	port = 4222
	if p := u.Port(); p != "" {
		// url.Parse takes any run of digits, so the range is checked here.
		if port, err = strconv.Atoi(p); err != nil || port < 1 || port > 65535 {
			return "", 0, fmt.Errorf("cannot read the port in %s '%s'", config.NATSURL, raw)
		}
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if !loopback(host) {
		return "", 0, fmt.Errorf("refusing to listen on '%s': %s is %s, and this version "+
			"binds loopback only. The broker has no authentication, so an address other "+
			"than 127.0.0.1 or ::1 would offer every queue to the network. Edit %s in %s",
			host, config.NATSURL, raw, config.NATSURL, config.File())
	}
	if err := refuseListen(host, port); err != nil {
		return "", 0, err
	}
	return host, port, nil
}

// refuseListen is a seam, in the shape of daemonCommand and selfCommand. It
// answers nothing in the product.
//
// It exists so a test floor can refuse the product's default port: every real
// broker boot in this package goes through listenAddr, and a case that makes
// its own scratch home with no config resolves nats_url to
// nats://127.0.0.1:4222, which is a live deployment's address on the machine
// running the tests.
//
// It is one variable for the whole package. A case that sets it aside must not
// run in parallel with a case that boots a broker, because the second case
// would then boot with no floor under it.
var refuseListen = func(host string, port int) error { return nil }

// loopback reports whether a host name reaches this machine and no other.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// dialAddr is the address `loc start` and `loc stop` probe.
func dialAddr(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }

// portAnswers reports whether something listens at addr.
func portAnswers(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// serveDaemon is `loc start --serve`: the broker and the heartbeat, in this
// process, until a signal ends it.
//
// The prose the sweep prints goes to w. The daemon's own stream is the log the
// parent opened for it, so a reaped seat is on the record without a second
// logging path.
func serveDaemon(w io.Writer) error {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, stopSignals()...)
	defer signal.Stop(sigs)
	return runDaemon(w, beatInterval, sigs, nil)
}

// runDaemon is the daemon with its period, its endings and its ready signal
// handed in, so a test drives the whole of it in-process on a short beat.
//
// ready, when it is given, is called once the broker answers and the pidfile
// is written.
func runDaemon(w io.Writer, beat time.Duration, sigs <-chan os.Signal, ready func()) error {
	home := config.Home()
	beatDir := filepath.Join(home, "run", "heartbeat")
	pruneHeartbeats(beatDir, config.Int(config.HeartbeatLogRetentionDays), time.Now())

	host, port, err := listenAddr()
	if err != nil {
		return err
	}

	// THE SERVER MAKES store/ ITSELF. JetStream creates its store directory on
	// boot, so a MkdirAll here would only be a second place that decides where
	// the store lives.
	srv, err := natsserver.NewServer(broker.Options(host, port, filepath.Join(home, "store")))
	if err != nil {
		return fmt.Errorf("cannot start the broker: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(readyWait) {
		srv.Shutdown()
		return fmt.Errorf("the broker did not answer on %s within %s", dialAddr(host, port), readyWait)
	}

	if err := ensureTopics(srv.ClientURL()); err != nil {
		srv.Shutdown()
		srv.WaitForShutdown()
		return err
	}

	if err := writeDaemonPID(); err != nil {
		srv.Shutdown()
		srv.WaitForShutdown()
		return err
	}
	if ready != nil {
		ready()
	}

	beatOnce(w, beatDir)

	ticker := time.NewTicker(beat)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			beatOnce(w, beatDir)
		case <-sigs:
			// ONCE THE END IS DECIDED, LATER SIGNALS ARE IGNORED. The shutdown
			// and the pidfile removal run after this line, and the default
			// disposition would end the process in the middle of them.
			signal.Ignore(stopSignals()...)
			ticker.Stop()
			srv.Shutdown()
			srv.WaitForShutdown()
			return releaseDaemonPID()
		}
	}
}

// beatOnce sweeps and writes the one line that says it happened.
//
// A QUIET BEAT WRITES A LINE TOO. A log that only records repairs cannot tell
// a heartbeat that found nothing from a heartbeat that never ran, and the
// second is the failure worth seeing.
func beatOnce(w io.Writer, beatDir string) {
	changes, err := sweepVerb(w, nil)
	if err != nil {
		appendBeat(beatDir, fmt.Sprintf("sweep failed: %v", err))
		return
	}
	appendBeat(beatDir, fmt.Sprintf("swept, %d changes", changes))
}

// appendBeat adds one line to today's heartbeat log.
func appendBeat(beatDir, what string) {
	if err := os.MkdirAll(beatDir, 0o700); err != nil {
		return
	}
	now := time.Now()
	path := filepath.Join(beatDir, now.Format("2006-01-02")+".log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", now.Format(time.RFC3339), what)
}

// pruneHeartbeats deletes the logs older than heartbeat_log_retention_days.
//
// The growth of the directory is bounded by that number of files and by no
// other mechanism. There is no rotation tool.
func pruneHeartbeats(beatDir string, days int, now time.Time) {
	if days <= 0 {
		return
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	entries, err := os.ReadDir(beatDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(beatDir, e.Name()))
	}
}

// ensureTopics creates the TOPICS stream when the store does not hold it.
//
// The stream is what makes a room a room, and nothing in the product created
// it until now: a deployment got it from the conformance suite or from a hand
// run of the `nats` tool. Its configuration is the one that suite uses —
// subjects topic.>, limits retention, the configured window, file storage, one
// replica.
func ensureTopics(clientURL string) error {
	nc, err := nats.Dial(clientURL, natsgo.Name("loc-daemon"), natsgo.Timeout(readyWait))
	if err != nil {
		return fmt.Errorf("cannot reach the broker just started: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("cannot open JetStream: %v", err)
	}
	if _, err := js.StreamInfo(topicsStream); err == nil {
		return nil
	}
	window, err := parseWindow(config.Value(config.TopicWindow))
	if err != nil {
		return err
	}
	_, err = js.AddStream(&natsgo.StreamConfig{
		Name:      topicsStream,
		Subjects:  []string{"topic.>"},
		Retention: natsgo.LimitsPolicy,
		MaxAge:    window,
		Storage:   natsgo.FileStorage,
		Replicas:  1,
	})
	if err != nil {
		return fmt.Errorf("cannot create the %s stream: %v", topicsStream, err)
	}
	return nil
}

// topicsStream is the one stream that holds every room.
const topicsStream = "TOPICS"

// parseWindow reads a retention window.
//
// The value is a person's, and a person writes 7d. Go's own parser stops at
// hours, so the day suffix is read here and everything else is handed to it.
func parseWindow(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("cannot read %s '%s'", config.TopicWindow, v)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("cannot read %s '%s'", config.TopicWindow, v)
	}
	return d, nil
}

// writeDaemonPID records this process, in the shape ServerPIDFile uses: the
// pid, then the start time the operating system reports for it.
//
// THE PAIR IS THE IDENTITY. A number alone reads as alive the moment the
// kernel hands it to a stranger, and `loc start` would then refuse to boot
// because of a process that is not the daemon.
func writeDaemonPID() error {
	path := model.DaemonPIDFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	pid := os.Getpid()
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n%s\n", pid, model.StartedAt(pid))), 0o600)
}

// releaseDaemonPID removes the pidfile. A file already gone is a success: the
// caller asked for the deployment to name no daemon, and it names none.
func releaseDaemonPID() error {
	if err := os.Remove(model.DaemonPIDFile()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// liveDaemon reports the daemon's pid when the process the pidfile names is
// still the one that wrote it.
//
// THE PAIR IS THE IDENTITY, and the read is the report's own (readDaemonPID in
// cmd/loc/presence.go), so `loc status`, `loc start` and `loc stop` cannot
// disagree about whether the house is up.
func liveDaemon() (int, bool) {
	pid, started, ok := readDaemonPID()
	if !ok || !model.Alive(pid, started) {
		return 0, false
	}
	return pid, true
}
