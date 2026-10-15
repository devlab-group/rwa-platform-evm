package models

// ComplianceStatus mirrors IComplianceRegistry.ComplianceStatus and is shared by
// Investor read models and ComplianceOperation write records
type ComplianceStatus string

const (
	ComplianceUnknown ComplianceStatus = "Unknown"
	ComplianceAllowed ComplianceStatus = "Allowed"
	ComplianceBlocked ComplianceStatus = "Blocked"
)
