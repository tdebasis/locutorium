package mcpserve

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// THE BELL RINGS FROM INSIDE THE BINARY.
//
// A shell hook cannot cross to Windows and CI runs none of it. The bell is
// therefore a Go function here, chosen per seat by the listener type the seat
// registered with (R28, 2026-09-09). The product carries no seat names: a
// deployment says how a seat is reached by setting LOC_LISTENER_TYPE, and the
// type is the whole of the selection.
//
// There is NO FALLBACK from one notifier to another, by the ruling of
// 2026-09-07. A bell that could not ring is written to the delivery log and
// the message waits in the queue.

// Notifier rings one seat's bell. The endpoint is the seat, the address is
// what the seat registered as its LOC_LISTENER_ADDRESS, and the bell is the
// one line to deliver.
//
// courier is what the delivering session should be CALLED, which the bell
// works out from the senders (#124). It is a label on the carrier and never
// part of the bell: a notifier that has no session to name ignores it.
type Notifier interface {
	Ring(endpoint, address, bell, courier string) error
}

// The four listener types. A deployment sets one of these in
// LOC_LISTENER_TYPE, and `subscribe` refuses anything else.
const (
	ListenerTmux    = "tmux"
	ListenerClaude  = "claude"
	ListenerWebhook = "webhook"
	ListenerNone    = "none"
)

// ListenerTypes is the closed set, in the order a refusal names them.
var ListenerTypes = []string{ListenerTmux, ListenerClaude, ListenerWebhook, ListenerNone}

// ValidListenerType reports whether a registered type has a notifier behind
// it. A type with no notifier is refused at `subscribe`, because a seat
// registered under it would read as reachable and ring nothing.
func ValidListenerType(t string) error {
	for _, v := range ListenerTypes {
		if t == v {
			return nil
		}
	}
	return fmt.Errorf("unknown listener type '%s': the valid types are %s",
		t, strings.Join(ListenerTypes, ", "))
}

// ValidListenerAddress reports whether a type can use an address. It is asked
// before a seat registers, by `subscribe` and by `loc mcp`, because a seat
// registered with an address its bell must refuse would read as reachable and
// every ring would fail.
//
// ONLY THE WEBHOOK TYPE HAS A RULE. A pane target and a pane id are opaque to
// this binary until tmux or the runtime reads them, so the other types accept
// any address here and their notifiers refuse an empty one at the ring.
func ValidListenerAddress(listenerType, address string) error {
	if listenerType != ListenerWebhook {
		return nil
	}
	_, err := webhookURL(address)
	return err
}

// ErrBusy marks a refusal that a LATER RING CAN REPAIR. The pane was in copy
// mode, or it was not at an empty prompt. Both states say the seat is busy
// right now, and both end on their own, so the bell asks again rather than
// leaving the mail with nothing to announce it. A message waited 3m57s that
// way.
//
// The webhook notifier marks two answers the same way: 429 and 503 are a
// receiver saying it cannot take the request now.
//
// NOTHING ELSE WRAPS IT. A missing pane address, a tmux that cannot be read,
// a courier that exited non-zero and a receiver that cannot be reached are all
// broken rather than busy, and asking again repairs none of them.
var ErrBusy = errors.New("the seat is busy")

// busyRefusal carries ErrBusy under a refusal WHOSE TEXT DOES NOT CHANGE. The
// reason reaches the day's record and `loc transcript` prints it, so the
// marker rides on the type rather than on another word in the string.
type busyRefusal struct{ err error }

func (b busyRefusal) Error() string { return b.err.Error() }

func (b busyRefusal) Unwrap() error { return b.err }

func (busyRefusal) Is(target error) bool { return target == ErrBusy }

// courierWindow is the rate limit under the claude notifier: at most one
// courier spawn per seat per this long (R35c). A courier is a whole Claude
// session, so a queue that fills quickly would otherwise spawn one session per
// arrival. A dropped ring loses nothing, because the message is in the queue
// and the next read finds it.
const courierWindow = 30 * time.Second

// paneSettleWait is the pause between typing the line and submitting it, as
// bin/tmux-msg does. The Enter is a separate send-keys because a pane that
// took the text but not the newline is recoverable, and one that took a
// half-typed line and a newline is not.
const paneSettleWait = 300 * time.Millisecond

// promptMode matches the mode indicator that the shell library's
// pane_is_claude_input requires. Both markers must be present in the pane, and
// they are here for the same reason they are there: either marker alone
// appears in ordinary printed output.
var promptMode = regexp.MustCompile(`(⏵⏵|⏸).*mode on`)

// newNotifier returns the notifier for a registered listener type. The log
// function is the seat's delivery log and now is the server's clock, so a test
// reads what a deployment reads and controls the rate limit's time.
func newNotifier(listenerType string, log func(string), now func() time.Time) (Notifier, error) {
	if err := ValidListenerType(listenerType); err != nil {
		return nil, err
	}
	switch listenerType {
	case ListenerTmux:
		return &tmuxNotifier{log: log}, nil
	case ListenerClaude:
		return &courierNotifier{log: log, now: now, last: map[string]time.Time{}}, nil
	case ListenerWebhook:
		return newWebhookNotifier(log), nil
	default:
		return silentNotifier{}, nil
	}
}

// ---------------------------------------------------------------- tmux

// tmuxNotifier types the bell into the seat's pane.
type tmuxNotifier struct{ log func(string) }

// Ring types one line into the pane and submits it.
//
// THE GUARD IS THE ONE PART OF THIS FILE THAT IS NOT OPTIONAL. A pane that has
// dropped to a shell EXECUTES what is typed into it. Two questions are asked
// before a keystroke is written, and both come from the shell library this
// replaces. The first is #{pane_in_mode}: a pane in copy mode passes the
// prompt check and is not a prompt, which was measured on 2026-09-08. The
// second is the prompt marker pair, which is what tells a Claude input box
// from a shell.
//
// THE COPY-MODE CHECK RUNS FIRST ON PURPOSE. Copy mode is how a person reads a
// seat's pane: they scroll back through it. It is therefore the one signal
// this notifier has that a human is reading the pane right now, and the guard
// refuses rather than types over their reading.
//
// WHAT THE GUARD DOES NOT ESTABLISH. Both prompt markers are present while
// Claude is mid-turn, measured 2026-09-05 and recorded in the deployment's
// HOUSE-RULES. The guard therefore cannot see a human who is mid-sentence in
// the input box, and a collision with a half-typed line is accepted by the
// PLAYBOOK's standing ruling rather than prevented here.
//
// THE 30-SECOND THROTTLE BELOW IS THE COURIER'S ALONE. This path is
// unthrottled, by that same ruling.
//
// THE TWO GUARD REFUSALS ARE MARKED ErrBusy and the other three refusals are
// not, so the bell can tell a seat that is busy from a bell that is broken.
func (n *tmuxNotifier) Ring(endpoint, address, bell, _ string) error {
	if address == "" {
		return n.refuse(endpoint, "no pane address")
	}
	mode, err := output("tmux", "display", "-p", "-t", address, "#{pane_in_mode}")
	if err != nil {
		return n.refuse(endpoint, fmt.Sprintf("cannot read pane %s: %v", address, err))
	}
	if strings.TrimSpace(mode) != "0" {
		return busyRefusal{n.refuse(endpoint, "pane_in_mode")}
	}
	pane, err := output("tmux", "capture-pane", "-t", address, "-p")
	if err != nil {
		return n.refuse(endpoint, fmt.Sprintf("cannot capture pane %s: %v", address, err))
	}
	if !isPrompt(pane) {
		return busyRefusal{n.refuse(endpoint, "not a prompt")}
	}
	if err := exec.Command("tmux", "send-keys", "-t", address, "-l", "--", bell).Run(); err != nil {
		return n.fail(endpoint, fmt.Sprintf("send-keys: %v", err))
	}
	time.Sleep(paneSettleWait)
	if err := exec.Command("tmux", "send-keys", "-t", address, "Enter").Run(); err != nil {
		return n.fail(endpoint, fmt.Sprintf("send-keys Enter: %v", err))
	}
	n.log(fmt.Sprintf("nudge %s rang '%s'", endpoint, bell))
	return nil
}

func (n *tmuxNotifier) refuse(endpoint, reason string) error {
	n.log(fmt.Sprintf("nudge %s refused: %s", endpoint, reason))
	return fmt.Errorf("refused: %s", reason)
}

func (n *tmuxNotifier) fail(endpoint, reason string) error {
	n.log(fmt.Sprintf("nudge %s BELL FAILED: %s", endpoint, reason))
	return errors.New(reason)
}

// isPrompt reports whether a captured pane is a Claude input box. It reads the
// last twelve non-blank lines and requires both markers, which is what the
// shell library's pane_is_claude_input requires.
func isPrompt(pane string) bool {
	var lines []string
	for _, l := range strings.Split(pane, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return false
	}
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	var mode, prompt bool
	for _, l := range lines {
		if promptMode.MatchString(l) {
			mode = true
		}
		if strings.HasPrefix(l, "❯") {
			prompt = true
		}
	}
	return mode && prompt
}

// ---------------------------------------------------------------- claude

// courierNotifier runs a one-shot `claude -p` that delivers the bell through
// the runtime's own session messaging. Nothing is typed into a pane.
type courierNotifier struct {
	log func(string)
	now func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

// courierPrompt is the fixed text, carried from the deployment's nudge hook.
// It names a pane token and the bell and nothing else: a courier that was
// given the message body once reasoned about it and acted on its own judgment,
// so there is nothing here to reason about.
const courierPrompt = "You are a one-shot wake courier. Call ListAgents. " +
	"Find the live session whose tmux pane id is '%s'. " +
	"Use SendMessage to send that session exactly this message: '[LOC] %s — run /read-loc now.'. " +
	"Send to no other session. If no session matches, do nothing. Then stop."

// Ring spawns the courier, at most once per seat per courierWindow.
//
// THE TWO ENVIRONMENT GUARDS ARE LOAD-BEARING. The courier is a full Claude
// session. On 2026-08-16 one fired the recipient's own hooks and CONSUMED the
// message it was announcing. LOC_COURIER=1 makes the deployment's ingest hook
// exit early, and a blanked LOC_IDENTITY leaves it nothing to read as. Either
// alone stops that; removing one must not silently reopen the hole.
func (n *courierNotifier) Ring(endpoint, address, bell, courier string) error {
	if address == "" {
		return n.fail(endpoint, "no pane address")
	}
	if !n.take(endpoint) {
		n.log(fmt.Sprintf("nudge %s rate-limited", endpoint))
		return nil
	}
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return n.fail(endpoint, fmt.Sprintf("open %s: %v", os.DevNull, err))
	}
	defer devnull.Close()

	// THE COURIER IS NAMED AFTER THE SENDER. The receiving pane labels the
	// message with this session's display name, and without -n the runtime
	// generates one, which puts noise in the one field a reader checks for
	// "who is this from" (#124). The bell text is untouched: the name is on
	// the session, not in the line.
	args := make([]string, 0, 6)
	if courier != "" {
		args = append(args, "-n", courier)
	}
	args = append(args, "-p", fmt.Sprintf(courierPrompt, address, bell),
		"--allowedTools", "ListAgents,SendMessage")
	cmd := exec.Command("claude", args...)
	cmd.Stdin = devnull
	cmd.Env = append(os.Environ(), "LOC_COURIER=1", "LOC_IDENTITY=")
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return n.fail(endpoint, fmt.Sprintf("courier exit %d", ee.ExitCode()))
		}
		return n.fail(endpoint, fmt.Sprintf("courier: %v", err))
	}
	n.log(fmt.Sprintf("nudge %s rang '%s'", endpoint, bell))
	return nil
}

// take reports whether this seat may spawn now, and records the spawn when it
// may. The stamp is taken BEFORE the courier runs: a session takes seconds to
// start, and a window that opened only once the previous one finished would
// let a burst spawn several at once.
func (n *courierNotifier) take(endpoint string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.now()
	if last, ok := n.last[endpoint]; ok && now.Sub(last) < courierWindow {
		return false
	}
	n.last[endpoint] = now
	return true
}

func (n *courierNotifier) fail(endpoint, reason string) error {
	n.log(fmt.Sprintf("nudge %s BELL FAILED: %s", endpoint, reason))
	return errors.New(reason)
}

// ---------------------------------------------------------------- webhook

// webhookTimeout is how long the receiver has to answer. The bell is one
// small request to this machine, and the bell loop waits on it, so a receiver
// that holds the connection must not hold the loop.
const webhookTimeout = 5 * time.Second

// webhookEvent is the event_type of every bell. A receiver that takes several
// kinds of request on one URL selects on it.
const webhookEvent = "loc.bell"

// webhookBell is the body of the request. It carries the seat and the bell
// line and NEVER THE MESSAGE: the seat takes its mail through `read`, as every
// seat does, so the bell gives a receiver nothing to act on except the fact
// that mail arrived.
type webhookBell struct {
	EventType string `json:"event_type"`
	Endpoint  string `json:"endpoint"`
	Bell      string `json:"bell"`
}

// webhookNotifier sends the bell as one HTTP POST to the seat's address.
//
// THE REQUEST STAYS ON THIS MACHINE. The address must be a loopback URL, the
// client follows no redirect, and it uses no proxy. The request is not signed
// and carries no token, which is the broker's own posture on the loopback
// listener (R12): the machine is the boundary. Nothing here is meant to reach
// another computer.
type webhookNotifier struct {
	log    func(string)
	client *http.Client
}

func newWebhookNotifier(log func(string)) *webhookNotifier {
	return &webhookNotifier{log: log, client: &http.Client{
		Timeout: webhookTimeout,
		// A redirect can name another machine. The 3xx answer is returned as
		// it is, and Ring records it as a failed bell.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		// No proxy from the environment: a proxy is another hop, and the
		// loopback rule is about where the request goes.
		Transport: &http.Transport{Proxy: nil},
	}}
}

// webhookURL parses a webhook address and applies the loopback rule. The
// refusals name the rule and the host and never the whole address, because an
// address with user information in it holds a password.
func webhookURL(address string) (*url.URL, error) {
	if address == "" {
		return nil, errors.New("invalid webhook address: it is empty")
	}
	u, err := url.Parse(address)
	if err != nil {
		return nil, errors.New("invalid webhook address: it is not a URL")
	}
	if u.Scheme != "http" {
		return nil, fmt.Errorf("invalid webhook address: the scheme is '%s' and must be http", u.Scheme)
	}
	if u.User != nil {
		return nil, errors.New("invalid webhook address: it must not carry user information")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("invalid webhook address: it names no host")
	}
	if !loopbackHost(host) {
		return nil, fmt.Errorf("invalid webhook address: host '%s' is not loopback, "+
			"and the webhook bell stays on this machine", host)
	}
	return u, nil
}

// loopbackHost reports whether a host name reaches this machine and no other.
// It is the rule `loc start` applies to the broker's own listener.
func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Ring posts the bell and reads the status, and nothing else, of the answer.
//
// 2xx rang. 429 and 503 are a receiver that is busy, marked ErrBusy so the
// bell asks again. Every other answer, and no answer, is a failed bell.
//
// THE RESPONSE BODY IS NEVER RECORDED. The reason returned here reaches the
// day's record and `loc transcript` prints it, and a receiver's error page is
// the receiver's text and not this seat's. The status code is the whole of
// what is kept. The transport's own error text is left out for the same
// reason the bell leaves out a client's: it is written by another layer.
func (n *webhookNotifier) Ring(endpoint, address, bell, _ string) error {
	u, err := webhookURL(address)
	if err != nil {
		return n.fail(endpoint, err.Error())
	}
	body, err := json.Marshal(webhookBell{EventType: webhookEvent, Endpoint: endpoint, Bell: bell})
	if err != nil {
		return n.fail(endpoint, "webhook: the bell could not be encoded")
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return n.fail(endpoint, "webhook: the request could not be built")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && ue.Timeout() {
			return n.fail(endpoint, fmt.Sprintf("webhook: no answer in %s", n.client.Timeout))
		}
		return n.fail(endpoint, "webhook: the receiver could not be reached")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		n.log(fmt.Sprintf("nudge %s rang '%s'", endpoint, bell))
		return nil
	case code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable:
		return busyRefusal{n.refuse(endpoint, fmt.Sprintf("webhook status %d", code))}
	default:
		return n.fail(endpoint, fmt.Sprintf("webhook status %d", code))
	}
}

func (n *webhookNotifier) refuse(endpoint, reason string) error {
	n.log(fmt.Sprintf("nudge %s refused: %s", endpoint, reason))
	return fmt.Errorf("refused: %s", reason)
}

func (n *webhookNotifier) fail(endpoint, reason string) error {
	n.log(fmt.Sprintf("nudge %s BELL FAILED: %s", endpoint, reason))
	return errors.New(reason)
}

// ---------------------------------------------------------------- none

// silentNotifier rings nothing. A seat registered as `none` finds its mail on
// its next read. It is a deployment's deliberate choice and never a fallback
// the product picks by itself.
type silentNotifier struct{}

func (silentNotifier) Ring(_, _, _, _ string) error { return nil }

// output runs a command and returns its standard output.
func output(name string, args ...string) (string, error) {
	b, err := exec.Command(name, args...).Output()
	return string(b), err
}
