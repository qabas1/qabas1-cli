package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strings"
	"text/tabwriter"
	"text/template"
)

type Printer struct {
	JSON  bool
	Quiet bool
	Out   io.Writer
	Err   io.Writer
}

func New(jsonOut, quiet bool) *Printer {
	return &Printer{JSON: jsonOut, Quiet: quiet, Out: os.Stdout, Err: os.Stderr}
}

func (p *Printer) Print(a ...any) {
	if p.Quiet || p.JSON {
		return
	}
	fmt.Fprint(p.Out, a...)
}

func (p *Printer) Println(a ...any) {
	if p.Quiet || p.JSON {
		return
	}
	fmt.Fprintln(p.Out, a...)
}

func (p *Printer) Printf(format string, a ...any) {
	if p.Quiet || p.JSON {
		return
	}
	fmt.Fprintf(p.Out, format, a...)
}

func (p *Printer) Warnf(format string, a ...any) {
	if p.Quiet {
		return
	}
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}
	fmt.Fprintf(p.Err, format, a...)
}

func (p *Printer) Encode(v any) error {
	enc := json.NewEncoder(p.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (p *Printer) Result(human string, v any) error {
	if p.JSON {
		return p.Encode(v)
	}
	if p.Quiet {
		return nil
	}
	if human != "" {
		fmt.Fprintln(p.Out, human)
	}
	return nil
}

// Table writes an uppercase-header tab-aligned table (docker-style listings).
func Table(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}

var fieldRef = regexp.MustCompile(`\{\{\s*\.([A-Za-z0-9_]+)\s*\}\}`)

// FormatList renders a slice with a Docker-style --format Go template.
//
// Supported formats:
//   - "json" — JSON array
//   - "table" or "table {{.Field}}\t..." — aligned table (headers derived from fields)
//   - "{{.Field}}" — one line per item
//
// defaultTable is used when format is "" or "table" (should start with "table ").
func FormatList(w io.Writer, format, defaultTable string, items any) error {
	format = strings.TrimSpace(format)
	if format == "" {
		format = defaultTable
	}
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(items)
	}

	asTable := false
	tmplSrc := format
	if format == "table" {
		asTable = true
		tmplSrc = strings.TrimSpace(strings.TrimPrefix(defaultTable, "table"))
	} else if strings.HasPrefix(format, "table") {
		asTable = true
		tmplSrc = strings.TrimSpace(strings.TrimPrefix(format, "table"))
	}
	if tmplSrc == "" {
		return fmt.Errorf("empty format template")
	}

	tmpl, err := template.New("format").Funcs(template.FuncMap{
		"json": func(v any) (string, error) {
			b, err := json.Marshal(v)
			return string(b), err
		},
	}).Parse(tmplSrc)
	if err != nil {
		return fmt.Errorf("parse --format: %w", err)
	}

	rv := reflect.ValueOf(items)
	if rv.Kind() != reflect.Slice {
		return fmt.Errorf("FormatList expects a slice (got %T)", items)
	}

	if asTable {
		header := fieldRef.ReplaceAllStringFunc(tmplSrc, func(m string) string {
			sub := fieldRef.FindStringSubmatch(m)
			if len(sub) < 2 {
				return m
			}
			return strings.ToUpper(sub[1])
		})
		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, header)
		for i := 0; i < rv.Len(); i++ {
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, rv.Index(i).Interface()); err != nil {
				return err
			}
			fmt.Fprintln(tw, strings.TrimRight(buf.String(), "\n"))
		}
		return tw.Flush()
	}

	for i := 0; i < rv.Len(); i++ {
		if err := tmpl.Execute(w, rv.Index(i).Interface()); err != nil {
			return err
		}
		if !strings.HasSuffix(tmplSrc, "\n") {
			fmt.Fprintln(w)
		}
	}
	return nil
}
