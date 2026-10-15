package assets

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// unmarshalStrict rejects unknown struct fields and trailing JSON content, including in
// nested envelope objects. Plain json.Unmarshal would ignore fields the offline signer
// rejects.
func unmarshalStrict(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("unexpected trailing content after JSON value")
	}
	return nil
}
