// Package presence is the agent-presence model: which agents exist, whether
// their processes are alive, and what they are doing.
//
// THREE FACTS, THREE MECHANISMS, and none of them inferred from another. A
// registration is a record the supervisor wrote when it launched the agent;
// liveness is a question for the operating system about a recorded (pid, start
// time) pair; activity is whatever the agent's own last event said. The medium
// stores none of it — it carries events and mail, and this package holds the
// record the answers are read from. The CLI verbs are a thin skin over what is
// here.
package presence

import (
	"fmt"
	"regexp"
	"strings"
)

// segment is one part of an endpoint name. The character set is narrow on
// purpose: it is what makes the dots-to-underscores substitution used for
// backing-object names injective, because no endpoint can contain the
// underscore the substitution introduces.
const segment = `[a-z0-9-]+`

var (
	endpointName = regexp.MustCompile(`^` + segment + `\.` + segment + `$`)
	instanceName = regexp.MustCompile(`^` + segment + `$`)
)

// ValidEndpoint refuses anything that is not a fully-qualified endpoint.
//
// A bare agent name is never an endpoint: a consumer watching two instances
// could not tell two agents of the same name apart.
func ValidEndpoint(endpoint string) error {
	if !endpointName.MatchString(endpoint) {
		return fmt.Errorf("invalid endpoint name '%s': an endpoint must match <instance>.<agent>, "+
			"each segment [a-z0-9-]+, with exactly one dot and the underscore barred", endpoint)
	}
	return nil
}

// ValidInstance refuses anything that is not an instance namespace.
func ValidInstance(instance string) error {
	if !instanceName.MatchString(instance) {
		return fmt.Errorf("invalid instance name '%s': an instance must match [a-z0-9-]+", instance)
	}
	return nil
}

// Instance is an endpoint's prefix — the namespace whose event subject and
// registry request it belongs to. It is empty for anything unqualified.
func Instance(endpoint string) string {
	i := strings.IndexByte(endpoint, '.')
	if i < 0 {
		return ""
	}
	return endpoint[:i]
}

// StreamName is the backing object for an endpoint's queue. A subject may
// carry dots where a durable object's name may not, so the name is derived by
// substitution — injective, because of the character set above.
func StreamName(endpoint string) string {
	return "QUEUE_" + strings.ReplaceAll(endpoint, ".", "_")
}
