package view

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// field is one labelled value in a describe view.
type field struct {
	Key   string
	Value string
}

// section groups related fields under a heading.
type section struct {
	Name   string
	Fields []field
	// Lines carries free-form content (events, reasons) that does not fit the
	// key/value shape.
	Lines []string
}

// describe renders sections as an aligned, plain-text description. The output
// deliberately avoids colour tags: it is also what gets written to disk when
// the user saves the view.
func describe(title string, sections []section) string {
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", len([]rune(title))))
	b.WriteString("\n\n")

	for _, s := range sections {
		if len(s.Fields) == 0 && len(s.Lines) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s\n", strings.ToUpper(s.Name))

		width := 0
		for _, f := range s.Fields {
			if len(f.Key) > width {
				width = len(f.Key)
			}
		}
		for _, f := range s.Fields {
			value := f.Value
			if strings.TrimSpace(value) == "" {
				value = "-"
			}
			fmt.Fprintf(&b, "  %-*s  %s\n", width+1, f.Key+":", value)
		}
		for _, line := range s.Lines {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// toYAML renders a raw AWS SDK value as YAML, which is the closest analogue to
// the "y" view in k9s.
func toYAML(v any) string {
	out, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Sprintf("could not render YAML: %v", err)
	}
	return string(out)
}

// tagFields renders a tag map as sorted describe fields.
func tagFields(tags map[string]string) []field {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]field, 0, len(keys))
	for _, k := range keys {
		out = append(out, field{k, tags[k]})
	}
	return out
}
