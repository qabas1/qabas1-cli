package compose

import (
	"testing"

	"github.com/qabasone/qabasone-cli/internal/manifest"
)

func TestJSReportImagePin(t *testing.T) {
	rel := &manifest.Release{Infra: manifest.Infra{Jsreport: "jsreport/jsreport:4.15.0"}}
	if got := ImagePins(rel)["JSREPORT_IMAGE"]; got != rel.Infra.Jsreport {
		t.Fatalf("JSREPORT_IMAGE = %q", got)
	}
}
