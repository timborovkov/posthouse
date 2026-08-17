package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/timborovkov/posthouse/internal/autoconfig"
	"github.com/timborovkov/posthouse/internal/model"
)

func (c *CLI) schema(args []string) error {
	if len(args) == 0 || args[0] != "write" {
		return fmt.Errorf("usage: posthouse schema write --dir DIR")
	}
	flags := flag.NewFlagSet("schema write", flag.ContinueOnError)
	flags.SetOutput(c.stderr)
	dir := flags.String("dir", "", "directory for JSON Schema files")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("usage: posthouse schema write --dir DIR")
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}
	schemas := map[string]any{
		"connection-page":    schemaObject(model.ConnectionPage{}),
		"message-page":       schemaObject(model.MessagePage{}),
		"message-detail":     schemaObject(model.MessageDetail{}),
		"triage-page":        schemaObject(model.TriagePage{}),
		"prepared-operation": schemaObject(model.PreparedOperation{}),
		"operation-result":   schemaObject(model.OperationResult{}),
		"event-page":         schemaObject(model.EventPage{}),
		"autoconfig-result":  schemaObject(autoconfig.Result{}),
		"unread-summary":     schemaObject([]model.UnreadSummary{}),
	}
	written := make([]string, 0, len(schemas))
	for name, schema := range schemas {
		path := filepath.Join(*dir, "posthouse-"+name+".json")
		data, err := json.MarshalIndent(schema, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		written = append(written, path)
	}
	return writeJSON(c.stdout, map[string]any{"ok": true, "files": written})
}

func schemaObject(sample any) map[string]any {
	t := reflect.TypeOf(sample)
	result := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"title":   t.String(),
	}
	if t.Kind() == reflect.Slice {
		result["type"] = "array"
		result["items"] = schemaType(t.Elem())
		return result
	}
	result["type"] = "object"
	result["properties"] = schemaProperties(t)
	return result
}

func schemaProperties(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	properties := map[string]any{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := jsonFieldName(field)
		if name == "" || name == "-" {
			continue
		}
		if field.Anonymous {
			for key, value := range schemaProperties(field.Type) {
				properties[key] = value
			}
			continue
		}
		properties[name] = schemaType(field.Type)
	}
	return properties
}

func schemaType(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if t == reflect.TypeOf(time.Time{}) {
			return map[string]any{"type": "string", "format": "date-time"}
		}
		return map[string]any{"type": "object", "properties": schemaProperties(t)}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaType(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaType(t.Elem())}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	default:
		return map[string]any{}
	}
}

func jsonFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name
	}
	name, _, _ := strings.Cut(tag, ",")
	return name
}
