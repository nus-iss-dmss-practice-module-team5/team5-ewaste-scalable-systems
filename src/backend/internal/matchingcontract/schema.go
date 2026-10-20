// Package matchingcontract validates the byte-preserved approved C2/Task 5 schemas.
package matchingcontract

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Object is the decoded JSON contract value.
type Object = map[string]any

type object = Object

//go:embed schemas/*.json
var schemaFiles embed.FS

var schemas = compileSchemas()

func compileSchemas() map[string]*jsonschema.Schema {
	c := jsonschema.NewCompiler()
	c.AssertFormat = true
	c.LoadURL = func(string) (io.ReadCloser, error) { return nil, errors.New("remote schemas disabled") }
	entries, err := schemaFiles.ReadDir("schemas")
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		raw, err := schemaFiles.ReadFile("schemas/" + entry.Name())
		if err != nil {
			panic(err)
		}
		// Go's $ already means absolute end of text. Remove the equivalent
		// ECMAScript/Python negative lookahead only in the in-memory compiler input.
		raw = bytes.ReplaceAll(raw, []byte(`$(?![\\s\\S])`), []byte(`$`))
		if err := c.AddResource(entry.Name(), bytes.NewReader(raw)); err != nil {
			panic(err)
		}
	}
	result := map[string]*jsonschema.Schema{}
	for _, name := range []string{"MatchingInput", "MatchingOutput", "RequestSubmitted", "MatchingCompleted", "ClaimConfirmed", "CollectorAssigned", "CollectionCompleted", "CollectionFailed", "ReceiptVerified", "RecyclingCompleted", "RequestCompleted"} {
		result[name] = c.MustCompile(name + ".v1.schema.json")
	}
	return result
}

// Decode rejects duplicate keys and trailing JSON, preserving integer precision.
func Decode(raw []byte) (Object, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid contract")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 64 {
			return nil, errors.New("contract depth limit")
		}
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t {
		case json.Delim('{'):
			m := object{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, errors.New("invalid contract")
				}
				if _, exists := m[k]; exists {
					return nil, errors.New("invalid contract")
				}
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			_, err := d.Token()
			return m, err
		case json.Delim('['):
			a := make([]any, 0)
			for d.More() {
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err := d.Token()
			return a, err
		default:
			return t, nil
		}
	}
	v, err := read(0)
	if err != nil {
		return nil, errors.New("invalid contract")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, errors.New("invalid contract")
	}
	m, ok := v.(object)
	if !ok {
		return nil, errors.New("invalid contract")
	}
	return m, nil
}

// Validate rejects unknown contracts and never loads schemas over the network.
func Validate(name string, value map[string]any) error {
	schema, ok := schemas[name]
	if !ok {
		return errors.New("unsupported contract")
	}
	return schema.Validate(value)
}
