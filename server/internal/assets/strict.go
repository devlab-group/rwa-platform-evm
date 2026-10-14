package assets

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// unmarshalStrict decodes raw into v and rejects fields that v's struct type
// does not have and any content after the JSON value.
//
// Nested envelope objects (displayFields[], issuance, proofs[]) are decoded
// through it because json.Unmarshal ignores unknown fields, which would accept a
// document the offline signer's strict decoder rejects.
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
