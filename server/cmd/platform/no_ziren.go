//go:build ziren

package main

// Release builds must exclude ziren: it routes go-ethereum Keccak-256 through the Ziren zkVM. This
// undefined identifier rejects builds with that tag.
var _ = zirenBuildTagIsNotAllowed
