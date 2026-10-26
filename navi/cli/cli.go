// Package cli is the client half of a navi's command line: the roles that
// talk to a RUNNING navi rather than hosting one.
//
// A navi binary has one hosting role and several client roles. Hosting is the
// navi's own composition (its connectors, tools, model, and portals — see
// package navi); the client roles are the same for every navi, because they
// build no navi at all. A client needs no queue, datastore, provider, or
// model: everything it renders and every action it posts goes through the
// remote object surface and observation stream (ADR 0017).
//
// This package exists so a navi composed in its OWN repository can offer the
// client roles without copying them. `cmd/navi` is its first consumer, and a
// per-navi repo (e.g. navi-bastilli) is the reason it is public:
//
//	func main() {
//	    args := os.Args[1:]
//	    switch role(args) {
//	    case "connect":
//	        err = cli.Connect(args[1:])
//	    case "enqueue":
//	        err = cli.Enqueue(args[1:])
//	    case "health":
//	        err = cli.Health(args[1:])
//	    default:
//	        err = run() // host this navi
//	    }
//	}
//
// Every role takes the argument list AFTER its own name and returns an error;
// the caller decides how to report and exit. None of them calls os.Exit.
package cli

// This file holds the shared plumbing the roles agree on: where a navi is, and
// how a client proves who it is. Keeping it here rather than in each role is
// what makes `connect`, `enqueue`, and `health` resolve the same endpoint.

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/flintwillow-software/opennavi/portal"
	"github.com/flintwillow-software/opennavi/portal/gateway"
	"github.com/flintwillow-software/opennavi/principal"
)

// PortalEnv names the environment variable a client role reads for the navi's
// endpoint when no positional argument is given. All the client roles read it,
// so they agree on where a navi is.
const PortalEnv = "NAVI_PORTAL"

// PartitionArgs splits a role's argument list into flags and positionals, so a
// role may accept its flags and its positional argument in ANY order.
//
// It exists because flag.FlagSet.Parse stops at the first non-flag argument: a
// `connect <url> --key k.pem` would parse the URL and SILENTLY DROP the
// credential, leaving the client unauthenticated with no error. The caller
// therefore passes only the flags to fs.Parse and reads the positionals
// separately.
//
// The set of value-taking flags is DERIVED from fs rather than passed in, and
// that is deliberate: a hand-maintained list would be the same defect one level
// up — a role adds a flag, forgets to list it, and the helper mis-parses
// silently. Declaring the flag on fs is the only step, and it is already
// mandatory. Arity comes from flag.Value's documented contract: a Value whose
// IsBoolFlag reports true takes no value (the parser treats -name as
// -name=true), so it never consumes the following token.
//
// The returned positional list is the caller's to interpret — typically the
// endpoint or the prompt.
func PartitionArgs(args []string, fs *flag.FlagSet) (flags, positional []string) {
	takesValue := make(map[string]bool)
	fs.VisitAll(func(f *flag.Flag) {
		takesValue[f.Name] = flagTakesValue(f)
	})

	for i := 0; i < len(args); i++ {
		arg := args[i]
		// A lone "-" is a positional (stdin by convention), not a flag.
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		// Go's flag package accepts both -flag and --flag spellings, and
		// -flag=value carries its own value.
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if takesValue[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, positional
}

// firstArg returns the first positional argument, or "" when there is none.
func firstArg(positional []string) string {
	if len(positional) == 0 {
		return ""
	}
	return positional[0]
}

// flagTakesValue reports whether a declared flag consumes the next argument.
// It asks the flag package's own question: a Value with IsBoolFlag() true is
// parsed as -name=true and never takes the following token (see the flag.Value
// documentation), so every other type — string, int, duration — does.
func flagTakesValue(f *flag.Flag) bool {
	type boolFlagger interface{ IsBoolFlag() bool }
	if bf, ok := f.Value.(boolFlagger); ok {
		return !bf.IsBoolFlag()
	}
	return true
}

// ResolveEndpoint resolves the portal endpoint: the positional argument wins,
// then $NAVI_PORTAL.
func ResolveEndpoint(arg string) (string, error) {
	raw := strings.TrimSpace(arg)
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(PortalEnv))
	}
	return NormalizeURL(raw)
}

// NormalizeURL accepts what an operator types ("127.0.0.1:8090") as well as a
// full URL, and refuses to dial nothing.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("a portal URL is required — <command> http://host:port, or set $%s", PortalEnv)
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "http://" + raw
	}
	return strings.TrimRight(raw, "/"), nil
}

// Credential builds the credential a client presents from the flags a role
// exposes. A token is a bearer credential (an OIDC session); a principal plus
// a key is a signed request.
//
// Neither is an error: a gateway with a real authenticator refuses an
// unauthenticated connection (fail closed), and a development gateway accepts
// it, so the client says what it has and lets the navi decide. The second
// result describes the credential for the operator's orienting line.
func Credential(token, ref, keyPath, keyID string) (portal.CredentialSource, string, error) {
	switch {
	case token != "" && keyPath != "":
		return nil, "", fmt.Errorf("--token and --key are mutually exclusive")
	case token != "":
		return gateway.BearerToken(token), "bearer token", nil
	case keyPath != "":
		if ref == "" {
			return nil, "", fmt.Errorf("--key requires --principal")
		}
		if keyID == "" {
			return nil, "", fmt.Errorf("--key requires --key-id")
		}
		who, err := principal.Parse(ref)
		if err != nil {
			return nil, "", fmt.Errorf("--principal: %w", err)
		}
		key, err := gateway.LoadEd25519Key(keyPath)
		if err != nil {
			return nil, "", err
		}
		signer, err := gateway.Ed25519Signer(who, keyID, key)
		if err != nil {
			return nil, "", err
		}
		return signer, "signed request as " + who.String(), nil
	case ref != "":
		return nil, "", fmt.Errorf("--principal requires --key")
	}
	return nil, "no credential", nil
}
