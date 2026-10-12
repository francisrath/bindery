package main

import (
	"testing"

	"github.com/vavallee/bindery/internal/metadata/bnf"
	"github.com/vavallee/bindery/internal/metadata/dnb"
	"github.com/vavallee/bindery/internal/metadata/hardcover"
	"github.com/vavallee/bindery/internal/metadata/openlibrary"
)

// TestPrimaryMetadataProvider covers the wiring after resolution: each
// accepted value builds that provider, so a value the settings accept never
// silently boots OpenLibrary instead.
func TestPrimaryMetadataProvider(t *testing.T) {
	ol, dnbClient, hc := openlibrary.New(), dnb.New(), hardcover.New()
	for _, name := range []string{"openlibrary", "dnb", "nb", "bnf", "hardcover"} {
		if got := primaryMetadataProvider(name, ol, dnbClient, hc); got == nil || got.Name() != name {
			t.Errorf("primaryMetadataProvider(%q) = %v, want the %s provider", name, got, name)
		}
	}
	if _, ok := primaryMetadataProvider("bnf", ol, dnbClient, hc).(*bnf.Client); !ok {
		t.Error("bnf is not wired to the BnF client")
	}
	if got := primaryMetadataProvider("dnb", ol, dnbClient, hc); got != dnbClient {
		t.Error("dnb primary is a second DNB client, want the enricher's instance")
	}
}

// TestResolveMetadataPrimaryProvider covers the boot-time provider selection:
// openlibrary stays the default for every unset / unusable value, dnb and
// hardcover are honoured when selected, and a tokenless hardcover selection
// degrades to openlibrary instead of leaving every lookup unauthenticated.
func TestResolveMetadataPrimaryProvider(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		hasToken   bool
		want       string
	}{
		{"unset defaults to openlibrary", "", false, "openlibrary"},
		{"unset defaults to openlibrary even with a token", "", true, "openlibrary"},
		{"explicit openlibrary", "openlibrary", false, "openlibrary"},
		{"explicit dnb", "dnb", false, "dnb"},
		{"explicit nb", "nb", false, "nb"},
		{"explicit bnf", "bnf", false, "bnf"},
		{"hardcover with a token", "hardcover", true, "hardcover"},
		{"hardcover without a token falls back", "hardcover", false, "openlibrary"},
		{"unknown value falls back", "goodreads", true, "openlibrary"},
		{"enricher-only provider falls back", "googlebooks", true, "openlibrary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMetadataPrimaryProvider(tc.configured, tc.hasToken); got != tc.want {
				t.Errorf("resolveMetadataPrimaryProvider(%q, %v) = %q, want %q",
					tc.configured, tc.hasToken, got, tc.want)
			}
		})
	}
}
