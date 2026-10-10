package github

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultGHTokenTTL is how long a gh-CLI token is reused before gh is asked
// again.
const DefaultGHTokenTTL = 5 * time.Minute

// GHCLIToken is a TokenSource over the GitHub CLI's stored credentials: it runs
// `gh auth token`, so a developer who is already logged in needs no exported
// variable.
//
// It shells out rather than reading hosts.yml on purpose. Since gh 2.26 the
// token is kept in the system keyring by default — Keychain on macOS, wincred
// on Windows, Secret Service on Linux — and hosts.yml retains only host
// information, so parsing that file would silently find nothing for most users.
// `gh auth token` is gh's own interface and copes with the keyring,
// GH_CONFIG_DIR, and the multi-account config format. It also honours
// GH_TOKEN/GITHUB_TOKEN, which take precedence inside gh — so an exported token
// wins here too, matching gh's own order.
//
// This is a development convenience, not a production credential: prefer an
// explicit reference, or a GitHub App installation, for anything long-lived.
//
// A GHCLIToken holds a cache and must not be copied; hold it by pointer. The
// zero value is usable and reads github.com.
type GHCLIToken struct {
	// Host is the hostname to ask gh about. Empty = github.com.
	Host string
	// Path is the gh binary to run. Empty = "gh" from PATH.
	Path string
	// TTL is how long a fetched token is reused before gh is asked again, so a
	// re-login or a revoked token recovers without restarting the navi.
	// Zero = DefaultGHTokenTTL; negative asks gh on every call.
	TTL time.Duration

	mu      sync.Mutex
	token   string
	fetched time.Time
}

// Token implements TokenSource.
func (g *GHCLIToken) Token(ctx context.Context) (string, error) {
	if tok, ok := g.cached(); ok {
		return tok, nil
	}
	tok, err := g.fetch(ctx)
	if err != nil {
		return "", err
	}
	g.store(tok)
	return tok, nil
}

// cached returns a token that is still within its TTL.
func (g *GHCLIToken) cached() (string, bool) {
	ttl := g.TTL
	if ttl == 0 {
		ttl = DefaultGHTokenTTL
	}
	if ttl < 0 {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token == "" || time.Since(g.fetched) >= ttl {
		return "", false
	}
	return g.token, true
}

func (g *GHCLIToken) store(token string) {
	g.mu.Lock()
	g.token = token
	g.fetched = time.Now()
	g.mu.Unlock()
}

// fetch runs `gh auth token`. Only stderr is ever quoted in an error: stdout is
// where the token goes, so it must not reach a message.
func (g *GHCLIToken) fetch(ctx context.Context) (string, error) {
	bin := strings.TrimSpace(g.Path)
	if bin == "" {
		bin = "gh"
	}
	args := []string{"auth", "token"}
	if host := strings.TrimSpace(g.Host); host != "" && !strings.EqualFold(host, "github.com") {
		args = append(args, "--hostname", host)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// A bare name that is not on PATH and an absolute path that does not
		// exist are different mistakes with different fixes, so they are not
		// collapsed into one message.
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("github: %s not found on PATH; install the GitHub CLI, set GHCLIToken.Path, or use a credential reference instead", bin)
		}
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("github: %s does not exist; check GHCLIToken.Path", bin)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// gh prints "To get started with GitHub CLI, please run: gh auth login"
		// when nobody is logged in, which is exactly what the operator needs.
		return "", fmt.Errorf("github: %s %s failed: %s", bin, strings.Join(args, " "), msg)
	}
	token := strings.TrimSpace(stdout.String())
	if token == "" {
		return "", fmt.Errorf("github: %s %s printed no token; run `gh auth login`", bin, strings.Join(args, " "))
	}
	return token, nil
}

// ChainToken tries each source in order and returns the first token any of them
// yields. It is how a component expresses "an explicit reference, else the
// developer's gh login" without either source having to know about the other.
//
// When every source fails, the error names each attempt, so an operator sees
// all the places a credential could have come from rather than only the last.
func ChainToken(sources ...TokenSource) TokenSource {
	return chainToken(sources)
}

type chainToken []TokenSource

// Token implements TokenSource.
func (c chainToken) Token(ctx context.Context) (string, error) {
	if len(c) == 0 {
		return "", errors.New("github: no credential sources configured")
	}
	attempts := make([]string, 0, len(c))
	for _, src := range c {
		if src == nil {
			attempts = append(attempts, "a nil source")
			continue
		}
		token, err := src.Token(ctx)
		switch {
		case err != nil:
			attempts = append(attempts, err.Error())
		case strings.TrimSpace(token) == "":
			attempts = append(attempts, "a source returned an empty token")
		default:
			return token, nil
		}
	}
	return "", fmt.Errorf("github: no credential available (%s)", strings.Join(attempts, "; "))
}
