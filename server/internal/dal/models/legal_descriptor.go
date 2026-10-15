package models

// DocumentReference pins a document by the SHA-256 of its bytes
type DocumentReference struct {
	URI    string `json:"uri"`
	SHA256 string `json:"sha256"`
}

// Rating pairs the issuer's declared rating with its source document
type Rating struct {
	Provider string  `json:"provider"`
	Grade    *string `json:"grade,omitempty"`
	URI      string  `json:"uri"`
	SHA256   string  `json:"sha256"`
}

// LegalDescriptor holds optional profile.legal declarations about the instrument.
// The platform stores these issuer declarations without verifying them.
type LegalDescriptor struct {
	Wrapper         string             `json:"wrapper"`
	IssuerLEI       *string            `json:"issuerLei,omitempty"`
	HolderRights    *string            `json:"holderRights,omitempty"`
	RedemptionTerms *DocumentReference `json:"redemptionTerms,omitempty"`
	MaturityDate    *string            `json:"maturityDate,omitempty"`
	Rating          *Rating            `json:"rating,omitempty"`
}
