package compose

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestEnsureEdgeNetworkRepairsOnlyMissingConnections(t *testing.T) {
	var connected []string
	r := &Runtime{
		LookPath: func(string) (string, error) { return "docker", nil },
		Run: func(_ string, args []string, _ []string, stdout, stderr *bytes.Buffer) error {
			switch {
			case reflect.DeepEqual(args, []string{"network", "inspect", edgeNetwork}):
				return nil
			case len(args) == 6 && args[0] == "inspect":
				if args[5] == "edge-nginx" {
					_, _ = stdout.WriteString(`{"edge-network":{}}`)
				} else {
					_, _ = stdout.WriteString(`{"qabasone_default":{}}`)
				}
				return nil
			case len(args) == 4 && args[0] == "network" && args[1] == "connect":
				connected = append(connected, args[3])
				return nil
			}
			t.Fatalf("unexpected docker args: %v", args)
			return nil
		},
	}
	if err := r.EnsureEdgeNetwork(nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"qabasone-app", "qabasone-dash", "qabasone-api", "qabasone-minio", "qabasone-siraj"}
	if !reflect.DeepEqual(connected, want) {
		t.Fatalf("connected %v, want %v", connected, want)
	}
}

func TestEnsureEdgeNetworkReportsConnectFailure(t *testing.T) {
	r := &Runtime{
		LookPath: func(string) (string, error) { return "docker", nil },
		Run: func(_ string, args []string, _ []string, stdout, stderr *bytes.Buffer) error {
			if len(args) > 1 && args[1] == "inspect" && args[0] == "network" {
				return nil
			}
			if args[0] == "inspect" {
				_, _ = stdout.WriteString(`{}`)
				return nil
			}
			_, _ = stderr.WriteString("network connect failed")
			return errors.New("exit status 1")
		},
	}
	if err := r.EnsureEdgeNetwork(nil); err == nil || !strings.Contains(err.Error(), "network connect failed") {
		t.Fatalf("expected connection error, got %v", err)
	}
}
