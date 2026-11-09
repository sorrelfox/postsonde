package postman

import (
	"maps"
	"slices"
)

// SameDynamicNames are the Postman dynamic variables kept as written, for
// the tests.
func SameDynamicNames() []string { return slices.Sorted(maps.Keys(sameDynamic)) }
