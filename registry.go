// Package aipocket embeds the provider registry.
//
// The manifests are compiled into the binary and are never fetched at runtime.
// This is a deliberate security boundary: a manifest names the host an API key
// is sent to, so a registry that could be updated over the network would be a
// remote "send your credentials here" primitive. New providers ship with a new
// signed release, and nothing else.
package aipocket

import (
	"embed"

	"github.com/naturalmoods/aipocket/internal/manifest"
)

//go:embed all:providers
var providerFS embed.FS

// Registry parses and validates the embedded manifests. It is the whole public
// API: one function, so that "what can a program outside this module do with the
// registry" has a one-line answer. An exported accessor for the raw embedded
// filesystem lived here too, described as being for tooling and tests, and was
// used by neither — pre-1.0 is the only free moment to take that back.
func Registry() (*manifest.Registry, error) {
	return manifest.Load(providerFS, "providers")
}
