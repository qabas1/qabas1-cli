package compose

import (
	"reflect"
	"testing"
)

func TestServicesForImage(t *testing.T) {
	raw := []byte(`services:
  api:
    image: ${QABASONE_API_IMAGE:?required}
  new-worker:
    image: ${QABASONE_API_IMAGE}
  app:
    image: ${QABASONE_APP_IMAGE}
`)
	got, err := ServicesForImage(raw, "QABASONE_API_IMAGE")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"api", "new-worker"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("services = %v, want %v", got, want)
	}
	if _, err := ServicesForImage(raw, "UNKNOWN_IMAGE"); err == nil {
		t.Fatal("missing image mapping should fail before deployment")
	}
}
