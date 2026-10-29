package v1alpha1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"
)

// MaxSpecBytes bounds an offline specification, including whitespace.
const MaxSpecBytes = 1 << 20

// Decode accepts one UTF-8 JSON specification. Field names are case-sensitive;
// duplicate keys, null values and unknown fields are rejected before decoding
// into Go structs, avoiding encoding/json's implicit merging and last-key wins.
func Decode(r io.Reader) (Cluster, error) {
	var c Cluster
	data, err := io.ReadAll(io.LimitReader(r, MaxSpecBytes+1))
	if err != nil {
		return c, fmt.Errorf("read spec: %w", err)
	}
	if len(data) > MaxSpecBytes {
		return c, fmt.Errorf("spec exceeds %d bytes", MaxSpecBytes)
	}
	if !utf8.Valid(data) {
		return c, fmt.Errorf("spec must be valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := checkValue(d, reflect.TypeFor[Cluster](), "$"); err != nil {
		return c, err
	}
	if _, err := d.Token(); err != io.EOF {
		return c, fmt.Errorf("expected exactly one JSON document")
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return Cluster{}, err
	}
	if err := c.Validate(); err != nil {
		return Cluster{}, err
	}
	return c, nil
}

func checkValue(d *json.Decoder, typ reflect.Type, path string) error {
	token, err := d.Token()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if token == nil {
		return fmt.Errorf("%s: null is not allowed", path)
	}
	switch typ.Kind() {
	case reflect.Struct:
		if token != json.Delim('{') {
			return fmt.Errorf("%s: expected object", path)
		}
		fields := make(map[string]reflect.Type, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			fields[f.Tag.Get("json")] = f.Type
		}
		seen := make(map[string]bool)
		for d.More() {
			keyToken, err := d.Token()
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("%s: expected field name", path)
			}
			if seen[key] {
				return fmt.Errorf("%s.%s: duplicate field", path, key)
			}
			seen[key] = true
			fieldType, ok := fields[key]
			if !ok {
				return fmt.Errorf("%s.%s: unknown field (names are case-sensitive)", path, key)
			}
			if err := checkValue(d, fieldType, path+"."+key); err != nil {
				return err
			}
		}
		return closeValue(d, '}', path)
	case reflect.Slice:
		if token != json.Delim('[') {
			return fmt.Errorf("%s: expected array", path)
		}
		for i := 0; d.More(); i++ {
			if err := checkValue(d, typ.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return closeValue(d, ']', path)
	case reflect.String:
		if _, ok := token.(string); !ok {
			return fmt.Errorf("%s: expected string", path)
		}
	case reflect.Int:
		if _, ok := token.(json.Number); !ok {
			return fmt.Errorf("%s: expected integer", path)
		}
	default:
		return fmt.Errorf("%s: unsupported schema type %s", path, typ.Kind())
	}
	return nil
}

func closeValue(d *json.Decoder, want json.Delim, path string) error {
	token, err := d.Token()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if token != want {
		return fmt.Errorf("%s: expected %s", path, want)
	}
	return nil
}
