package repository

// Repo represents options that are specific to the repo storing backed up data.
type Options struct {
	User string `json:"user"`
	Host string `json:"host"`
}

type Maintenance struct {
	Type   MaintenanceType   `json:"type"`
	Safety MaintenanceSafety `json:"safety"`
	Force  bool              `json:"force"`
}

// ---------------------------------------------------------------------------
// Maintenance flags
// ---------------------------------------------------------------------------

type MaintenanceType int

// Can't be reordered as we rely on iota for numbering.
//
//go:generate stringer -type=MaintenanceType -linecomment
const (
	CompleteMaintenance MaintenanceType = iota // complete
	MetadataMaintenance                        // metadata
)

type MaintenanceSafety int

// Can't be reordered as we rely on iota for numbering.
//
//go:generate stringer -type=MaintenanceSafety -linecomment
const (
	FullMaintenanceSafety MaintenanceSafety = iota
	NoMaintenanceSafety
)