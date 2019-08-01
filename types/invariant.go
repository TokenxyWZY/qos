package types

import (
	"fmt"
	"github.com/QOSGroup/qbase/context"
)

// An Invariant is a function which tests a particular invariant.
// The invariant returns a descriptive message about what happened
// and total tokens
// and a boolean indicating whether the invariant has been broken.
// The simulator will then halt and print the logs.
type Invariant func(ctx context.Context) (string, int64, bool)

// Invariants defines a group of invariants
type Invariants []Invariant

// expected interface for registering invariants
type InvariantRegistry interface {
	RegisterRoute(moduleName, route string, invar Invariant)
}

// FormatInvariant returns a standardized invariant message along with
// a boolean indicating whether the invariant has been broken.
func FormatInvariant(module, name, msg string, broken bool) (string, bool) {
	return fmt.Sprintf("%s: %s invariant\n%sinvariant broken: %v\n",
		module, name, msg, broken), broken
}
