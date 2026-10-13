package huly

import (
	"fmt"
	"math/big"
	"strings"
)

// This file is the small piece of LexoRank the create path needs. Huly orders
// issues by a LexoRank: a bucket, a base36 value, and a trailing colon
// (`0|hzzzzz:`), compared lexicographically. Creating an issue means placing it
// AFTER the project's last one, which is LexoRank's `genNext`.
//
// The general algorithm is large (fractions, arbitrary insertion between two
// neighbours); only the append case is needed to create an issue, and for that
// `genNext` reduces to `ceil(value) + 8` — the gap Huly's own client leaves
// between consecutive ranks, visible in any project's ordering.

// rankMiddle is LexoRank's middle for bucket 0: the rank a project's first
// issue gets. It is half of the base36 space (`zzzzzz`), and matches the ranks
// Huly gives an issue with no neighbours.
const rankMiddle = "0|hzzzzz:"

// rankGap is the step LexoRank's genNext adds to an integer rank.
const rankGap = 8

// rankNext returns the rank that sorts after prev, or the middle when prev is
// empty. It implements LexoRank genNext for the append case.
func rankNext(prev string) (string, error) {
	prev = strings.TrimSpace(prev)
	if prev == "" {
		return rankMiddle, nil
	}
	bucket, value, err := splitRank(prev)
	if err != nil {
		return "", err
	}
	n, err := ceilBase36(value)
	if err != nil {
		return "", err
	}
	n.Add(n, big.NewInt(rankGap))
	return fmt.Sprintf("%s|%s:", bucket, n.Text(36)), nil
}

// splitRank splits `bucket|value:` into its bucket and its decimal value.
func splitRank(rank string) (bucket, value string, err error) {
	b, rest, ok := strings.Cut(rank, "|")
	if !ok || strings.TrimSpace(b) == "" {
		return "", "", fmt.Errorf("huly: rank %q has no bucket", rank)
	}
	rest = strings.TrimSuffix(rest, ":")
	if strings.TrimSpace(rest) == "" {
		return "", "", fmt.Errorf("huly: rank %q has no value", rank)
	}
	return b, rest, nil
}

// ceilBase36 parses a base36 decimal — an optional integer part, an optional
// radix point, and a fractional part — and rounds it UP to an integer, which is
// what genNext starts from.
func ceilBase36(value string) (*big.Int, error) {
	intPart, fracPart, _ := strings.Cut(value, ".")
	n := new(big.Int)
	if intPart != "" {
		if _, ok := n.SetString(intPart, 36); !ok {
			return nil, fmt.Errorf("huly: rank value %q is not base36", value)
		}
	}
	for _, r := range fracPart {
		if r != '0' {
			n.Add(n, big.NewInt(1))
			break
		}
	}
	return n, nil
}
