package assets

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// unmarshalStrict decodes raw into v rejecting any field not present on v's
// struct type(s) and any trailing garbage after the JSON value.
//
// profile.go and record.go unmarshal several nested envelope pieces
// (displayFields[], issuance, proofs[]) into Go structs. A plain
// json.Unmarshal silently drops fields it doesn't recognize, so a document
// carrying an extra key inside one of those objects would pass server
// validation while the offline signer's strict decoder rejects the same
// document — a parity gap. Every nested envelope decode MUST go through this
// helper instead of json.Unmarshal so both sides agree.
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
