package view

import (
	"encoding/json"
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
//
// The value is routed through JSON rather than handed straight to yaml.Marshal.
// Every AWS SDK v2 shape embeds an unexported marker struct
// (noSmithyDocumentSerde); yaml.v3 does not skip embedded unexported fields and
// panics with a reflect error when it tries to read one, and its own recover
// only handles yaml errors, so that panic escapes and takes the whole UI down.
// encoding/json ignores those fields, so the round trip yields a plain tree that
// yaml can always encode.
func toYAML(v any) string {
	out, err := marshalYAML(v)
	if err != nil {
		return fmt.Sprintf("could not render YAML: %v", err)
	}
	return out
}

// marshalYAML converts a value to YAML via a JSON round trip. A panic in either
// encoder is converted into an error: a malformed describe view is a far better
// outcome than a crashed session.
func marshalYAML(v any) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = "", fmt.Errorf("encoder panicked: %v", r)
		}
	}()

	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode json: %w", err)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return "", fmt.Errorf("decode json: %w", err)
	}

	encoded, err := yaml.Marshal(prune(tree))
	if err != nil {
		return "", fmt.Errorf("encode yaml: %w", err)
	}
	return string(encoded), nil
}

// prune drops unset values from a decoded JSON tree. AWS describe responses are
// mostly unset optional fields, and a screen of "null" and `Field: ""` buries
// the handful of values that matter.
//
// Nils, empty strings and empty collections go: in an AWS describe an absent
// value and an empty one mean the same thing, and the SDK's string-typed enums
// marshal to "" when unset. Zero values that do carry meaning — false and 0, as
// in EnableExecuteCommand or ExitCode — are kept.
func prune(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for key, value := range t {
			if pruned := prune(value); pruned != nil {
				out[key] = pruned
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, value := range t {
			if pruned := prune(value); pruned != nil {
				out = append(out, pruned)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return t
	default:
		return v
	}
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
