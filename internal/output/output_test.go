package output_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/output"
)

type row struct {
	ID      string
	Release string
}

func TestFormatListTable(t *testing.T) {
	var buf bytes.Buffer
	items := []row{{ID: "qb-1", Release: "2.8.0"}, {ID: "qb-2", Release: "2.9.0"}}
	err := output.FormatList(&buf, "", "table {{.ID}}\t{{.Release}}", items)
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "ID") || !strings.Contains(out, "RELEASE") {
		t.Fatalf("missing headers: %q", out)
	}
	if !strings.Contains(out, "qb-1") || !strings.Contains(out, "2.8.0") {
		t.Fatalf("missing rows: %q", out)
	}
}

func TestFormatListJSON(t *testing.T) {
	var buf bytes.Buffer
	items := []row{{ID: "qb-1", Release: "2.8.0"}}
	if err := output.FormatList(&buf, "json", "table {{.ID}}", items); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"ID"`) && !strings.Contains(buf.String(), `"id"`) {
		// encoding/json uses struct field names as-is when no tags
		if !strings.Contains(buf.String(), "qb-1") {
			t.Fatalf("bad json: %s", buf.String())
		}
	}
}

func TestFormatListRaw(t *testing.T) {
	var buf bytes.Buffer
	items := []row{{ID: "qb-1", Release: "2.8.0"}}
	if err := output.FormatList(&buf, "{{.ID}}", "table {{.ID}}", items); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "qb-1" {
		t.Fatalf("got %q", buf.String())
	}
}
