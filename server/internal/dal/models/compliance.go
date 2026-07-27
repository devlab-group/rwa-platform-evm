package models

// ComplianceStatus mirrors IComplianceRegistry.ComplianceStatus. It is the
// shared compliance-state enum used by both the Investor read model and the
// ComplianceOperation write record, so it lives here on its own rather than in
// either model's file.
type ComplianceStatus string

const (
	ComplianceUnknown ComplianceStatus = "Unknown"
	ComplianceAllowed ComplianceStatus = "Allowed"
	ComplianceBlocked ComplianceStatus = "Blocked"
)
