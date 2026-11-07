package assets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// unmarshalStrict rejects unknown fields, case variants and trailing JSON, including in
// nested envelope objects. This matches the signer; encoding/json alone accepts case
// variants such as "Amount" for "amount" and can overwrite the intended field.
func unmarshalStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("unexpected trailing content after JSON value")
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return err
	}
	return checkKeyCase(tree, reflect.TypeOf(v))
}

// checkKeyCase walks the decoded document alongside the Go type it was decoded into and
// requires every object key to equal a json field name exactly
func checkKeyCase(node any, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch n := node.(type) {
	case []any:
		if t.Kind() != reflect.Slice {
			return nil
		}
		for _, e := range n {
			if err := checkKeyCase(e, t.Elem()); err != nil {
				return err
			}
		}
	case map[string]any:
		if t.Kind() != reflect.Struct {
			return nil
		}
		fields := make(map[string]reflect.Type, t.NumField())
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
		}
		for k, e := range n {
			ft, ok := fields[k]
			if !ok {
				return fmt.Errorf("unknown field %q", k)
			}
			if err := checkKeyCase(e, ft); err != nil {
				return err
			}
		}
	}
	return nil
}
