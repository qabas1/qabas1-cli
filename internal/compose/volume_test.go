package compose

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTarVolumesFromUsesNamedVolumeWhenContainerMissing(t *testing.T) {
	var calls [][]string
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(_ string, args []string, _ []string, stdout, _ *bytes.Buffer) error {
			calls = append(calls, append([]string(nil), args...))
			joined := strings.Join(args, " ")
			switch {
			case strings.HasPrefix(joined, "inspect -f {{.Id}}"):
				return os.ErrNotExist // container missing
			case strings.Contains(joined, "inspect -f") && strings.Contains(joined, "Mounts"):
				return os.ErrNotExist
			case strings.HasPrefix(joined, "volume inspect"):
				name := args[len(args)-1]
				if name == ProjectName+"_"+MinioVolumeKey && stdout != nil {
					stdout.WriteString(name + "\n")
				}
				if name != ProjectName+"_"+MinioVolumeKey {
					return os.ErrNotExist
				}
				return nil
			case strings.HasPrefix(joined, "run --rm"):
				return nil
			default:
				return os.ErrNotExist
			}
		},
	}

	out := filepath.Join(t.TempDir(), "s3", "data.tar")
	if err := rt.TarVolumesFrom(MinioContainer, MinioDataDir, out, nil); err != nil {
		t.Fatal(err)
	}

	var runArgs []string
	for _, c := range calls {
		if len(c) > 0 && c[0] == "run" {
			runArgs = c
			break
		}
	}
	if runArgs == nil {
		t.Fatalf("expected docker run, got calls %#v", calls)
	}

	wantVol := ProjectName + "_" + MinioVolumeKey + ":" + MinioDataDir + ":ro"
	found := false
	for i := 0; i < len(runArgs)-1; i++ {
		if runArgs[i] == "-v" && runArgs[i+1] == wantVol {
			found = true
			break
		}
		if runArgs[i] == "--volumes-from" {
			t.Fatalf("should not use --volumes-from when container is missing: %#v", runArgs)
		}
	}
	if !found {
		t.Fatalf("expected volume mount %q in %#v", wantVol, runArgs)
	}
	assertMinioTmpExcluded(t, runArgs)
}

func TestTarVolumesFromFallsBackToVolumesFrom(t *testing.T) {
	var runArgs []string
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(_ string, args []string, _ []string, stdout, _ *bytes.Buffer) error {
			joined := strings.Join(args, " ")
			switch {
			case strings.Contains(joined, "Mounts"):
				return os.ErrNotExist // cannot resolve volume name
			case strings.HasPrefix(joined, "volume inspect"):
				return os.ErrNotExist
			case strings.HasPrefix(joined, "inspect -f {{.Id}}"):
				if stdout != nil {
					stdout.WriteString("abc123\n")
				}
				return nil
			case strings.HasPrefix(joined, "run --rm"):
				runArgs = append([]string(nil), args...)
				return nil
			default:
				return nil
			}
		},
	}

	out := filepath.Join(t.TempDir(), "data.tar")
	if err := rt.TarVolumesFrom(MinioContainer, MinioDataDir, out, nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := 0; i < len(runArgs)-1; i++ {
		if runArgs[i] == "--volumes-from" && runArgs[i+1] == MinioContainer {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected --volumes-from fallback, got %#v", runArgs)
	}
	assertMinioTmpExcluded(t, runArgs)
}

func assertMinioTmpExcluded(t *testing.T, args []string) {
	t.Helper()
	for _, arg := range args {
		if arg == "--exclude=./.minio.sys/tmp" {
			return
		}
	}
	t.Fatalf("expected MinIO transient files excluded from archive: %#v", args)
}

func TestResolveDataVolumePrefersInspect(t *testing.T) {
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(_ string, args []string, _ []string, stdout, _ *bytes.Buffer) error {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "Mounts") && stdout != nil {
				stdout.WriteString("custom_minio_vol\n")
			}
			return nil
		},
	}
	got, err := rt.ResolveDataVolume(MinioContainer, MinioDataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "custom_minio_vol" {
		t.Fatalf("got %q", got)
	}
}

func TestVolumeKeyForContainer(t *testing.T) {
	if got := volumeKeyForContainer(MinioContainer); got != MinioVolumeKey {
		t.Fatalf("got %q", got)
	}
	if got := volumeKeyForContainer("qabasone-e2e-minio"); got != MinioVolumeKey {
		t.Fatalf("suffix got %q", got)
	}
	if got := volumeKeyForContainer(JSReportContainer); got != JSReportVolumeKey {
		t.Fatalf("jsreport volume got %q", got)
	}
	if got := volumeKeyForContainer("qabasone-postgres"); got != "" {
		t.Fatalf("postgres should have no mapping, got %q", got)
	}
}
