package main

import (
	"log/slog"

	"github.com/vavallee/bindery/internal/api"
	"github.com/vavallee/bindery/internal/metadata"
	"github.com/vavallee/bindery/internal/metadata/bnf"
	"github.com/vavallee/bindery/internal/metadata/nb"
)

// primaryMetadataProvider is the provider wired as primary for name, a value
// resolveMetadataPrimaryProvider returned. The clients that are also
// enrichers are passed in so primary and enricher share one instance; NB and
// BnF are built here because they are never enrichers.
func primaryMetadataProvider(name string, openLibrary, dnbClient, hardcover metadata.Provider) metadata.Provider {
	switch name {
	case "dnb":
		return dnbClient
	case "nb":
		// Opt-in only: unlike the others, NB is never added as an enricher,
		// so an install that did not choose it sends NB no traffic. NB
		// publishes no rate limits for a fleet of independent installs.
		return nb.New()
	case "bnf":
		// Opt-in only, like NB: never an enricher, so an install that did not
		// choose it sends BnF no traffic. BnF publishes no rate limits; the
		// client paces itself at one request a second (internal/metadata/bnf).
		return bnf.New()
	case "hardcover":
		return hardcover
	default:
		return openLibrary
	}
}

// metadataPrimaryProviderDefault is the provider used when
// metadata.primary_provider is empty, unknown, or unusable.
const metadataPrimaryProviderDefault = "openlibrary"

// resolveMetadataPrimaryProvider maps the stored metadata.primary_provider
// value onto the provider that will actually be wired as primary.
//
// Two things can force a fallback to OpenLibrary:
//
//   - An unknown value. The settings API validates writes, but a value can
//     also arrive from a hand-edited database or a downgrade, and a primary
//     provider we can't construct would leave metadata dead.
//   - "hardcover" with no API token. Hardcover authenticates every GraphQL
//     query, search included, so a tokenless Hardcover primary fails on every
//     author and book lookup. The settings API refuses to store that
//     combination, but the token can still be removed out-of-band, so the
//     boot path degrades loudly instead of silently breaking metadata.
func resolveMetadataPrimaryProvider(configured string, hardcoverTokenConfigured bool) string {
	if configured == "" {
		return metadataPrimaryProviderDefault
	}
	if !api.IsMetadataPrimaryProviderValid(configured) {
		slog.Warn("unknown metadata.primary_provider — falling back to openlibrary",
			"configured", configured)
		return metadataPrimaryProviderDefault
	}
	if configured == "hardcover" && !hardcoverTokenConfigured {
		slog.Warn("metadata.primary_provider is hardcover but no Hardcover API token is configured — falling back to openlibrary (Hardcover authenticates every query, including search)")
		return metadataPrimaryProviderDefault
	}
	return configured
}
