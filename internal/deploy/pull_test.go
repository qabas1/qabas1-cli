package deploy

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/manifest"
)

func TestRetryPullResumesAfterTransientFailure(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	err := retryPull(func() error {
		attempts++
		if attempts < 3 {
			return errors.New("connection reset by peer")
		}
		return nil
	}, func(_ int, delay time.Duration) error { delays = append(delays, delay); return nil })
	if err != nil || attempts != 3 || !reflect.DeepEqual(delays, []time.Duration{3 * time.Second, 6 * time.Second}) {
		t.Fatalf("err=%v attempts=%d delays=%v", err, attempts, delays)
	}
}

func TestRetryPullStopsAfterBound(t *testing.T) {
	attempts := 0
	err := retryPull(func() error { attempts++; return errors.New("connection reset by peer") }, func(int, time.Duration) error { return nil })
	if attempts != 3 || err == nil || !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func baseRelease(api, app, dash, siraj string) *manifest.Release {
	return &manifest.Release{
		Components: manifest.Components{
			API:   manifest.Component{Digest: api},
			App:   manifest.Component{Digest: app},
			Dash:  manifest.Component{Digest: dash},
			Siraj: manifest.Component{Digest: siraj},
		},
	}
}

func TestChangedPullServicesProductOnly(t *testing.T) {
	current := baseRelease("sha256:a1", "sha256:b1", "sha256:c1", "sha256:d1")
	target := baseRelease("sha256:a2", "sha256:b2", "sha256:c2", "sha256:d2")

	got := changedPullServices(current, target)
	want := append([]string{}, compose.APIImageServices()...)
	want = append(want, "app", "dash", "siraj")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pull services\n got: %#v\nwant: %#v", got, want)
	}
}

func TestChangedPullServicesSkipsUnchangedInfra(t *testing.T) {
	current := baseRelease("sha256:a1", "sha256:b1", "sha256:c1", "sha256:d1")
	target := baseRelease("sha256:a2", "sha256:b1", "sha256:c1", "sha256:d1")
	// Empty infra on both sides must not pull minio/postgres/etc.
	current.Infra = manifest.Infra{}
	target.Infra = manifest.Infra{}

	got := changedPullServices(current, target)
	want := compose.APIImageServices()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pull services\n got: %#v\nwant: %#v", got, want)
	}
}

func TestChangedPullServicesInfraChange(t *testing.T) {
	current := baseRelease("sha256:a1", "sha256:b1", "sha256:c1", "sha256:d1")
	target := baseRelease("sha256:a1", "sha256:b1", "sha256:c1", "sha256:d1")
	current.Infra = manifest.Infra{Minio: "quay.io/minio/minio:old", Postgres: "postgres:16-alpine"}
	target.Infra = manifest.Infra{Minio: "quay.io/minio/minio:new", Postgres: "postgres:16-alpine"}

	got := changedPullServices(current, target)
	want := []string{"minio"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pull services\n got: %#v\nwant: %#v", got, want)
	}
}

func TestChangedPullServicesMinioMcMapsToMinio(t *testing.T) {
	current := baseRelease("sha256:a1", "sha256:b1", "sha256:c1", "sha256:d1")
	target := baseRelease("sha256:a1", "sha256:b1", "sha256:c1", "sha256:d1")
	current.Infra = manifest.Infra{MinioMc: "minio/mc:old"}
	target.Infra = manifest.Infra{MinioMc: "minio/mc:new"}

	got := changedPullServices(current, target)
	want := []string{"minio"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pull services\n got: %#v\nwant: %#v", got, want)
	}
}

func TestSetPullTargetsFirstInstall(t *testing.T) {
	plan := &Plan{Target: baseRelease("sha256:a", "sha256:b", "sha256:c", "sha256:d")}
	setPullTargets(plan, nil, false, false)
	if !plan.PullAll {
		t.Fatal("expected PullAll on first install")
	}
	if plan.PullServices != nil {
		t.Fatalf("expected nil PullServices, got %#v", plan.PullServices)
	}
}

func TestSetPullTargetsForce(t *testing.T) {
	current := baseRelease("sha256:a1", "sha256:b1", "sha256:c1", "sha256:d1")
	plan := &Plan{Target: baseRelease("sha256:a2", "sha256:b2", "sha256:c2", "sha256:d2")}
	setPullTargets(plan, current, true, true)
	if !plan.PullAll {
		t.Fatal("expected PullAll when force")
	}
}

func TestSetPullTargetsSelectiveUpgrade(t *testing.T) {
	current := baseRelease("sha256:a1", "sha256:b1", "sha256:c1", "sha256:d1")
	plan := &Plan{Target: baseRelease("sha256:a2", "sha256:b2", "sha256:c1", "sha256:d1")}
	setPullTargets(plan, current, true, false)
	if plan.PullAll {
		t.Fatal("expected selective pull, not PullAll")
	}
	want := append(compose.APIImageServices(), "app")
	if !reflect.DeepEqual(plan.PullServices, want) {
		t.Fatalf("pull services\n got: %#v\nwant: %#v", plan.PullServices, want)
	}
}
