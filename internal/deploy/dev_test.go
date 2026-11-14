package deploy

import (
	"testing"
	"time"

	"github.com/qabasone/qabasone-cli/internal/manifest"
)

func TestPromoteDevTargetAboveCurrentStable(t *testing.T) {
	now := time.Unix(1788253802, 0).UTC()
	target := &manifest.Release{
		Release: manifest.Meta{Version: "0.0.0-dev.1788253802", Channel: "dev"},
		Components: manifest.Components{
			API:   manifest.Component{Version: "0.0.0-dev.1788253802"},
			App:   manifest.Component{Version: "0.0.0-dev.1788253802"},
			Dash:  manifest.Component{Version: "0.0.0-dev.1788253802"},
			Siraj: manifest.Component{Version: "0.0.0-dev.1788253802"},
		},
	}

	promoteDevTarget(target, "1.0.4", now)

	if target.Release.Version != "1.0.5-dev.1788253802" {
		t.Fatalf("version %q", target.Release.Version)
	}
	if target.Components.API.Version != target.Release.Version {
		t.Fatalf("api version %q", target.Components.API.Version)
	}
}

func TestPromoteDevTargetKeepsNewerSemver(t *testing.T) {
	target := &manifest.Release{
		Release: manifest.Meta{Version: "2.0.0-dev.1", Channel: "dev"},
	}

	promoteDevTarget(target, "1.0.4", time.Unix(1788253802, 0).UTC())

	if target.Release.Version != "2.0.0-dev.1" {
		t.Fatalf("version %q", target.Release.Version)
	}
}
