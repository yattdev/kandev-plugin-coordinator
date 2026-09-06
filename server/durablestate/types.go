package durablestate

// RecordKind enumerates the materialized current-state record kinds named
// by STATE_COMPACTION_SPEC.md §1.1's full-snapshot schema. These are the
// only kinds the plugin storage layer recognizes; the runtime/scheduler
// owner decides what actually populates them.
type RecordKind string

const (
	KindFollowUp    RecordKind = "follow_up"
	KindLease       RecordKind = "lease"
	KindDirtyTask   RecordKind = "dirty_task"
	KindEscalation  RecordKind = "escalation"
	KindDoneReceipt RecordKind = "done_receipt"
)

// validRecordKinds is used to reject unknown kinds early (fail closed)
// rather than silently accepting an arbitrary string.
var validRecordKinds = map[RecordKind]bool{
	KindFollowUp:    true,
	KindLease:       true,
	KindDirtyTask:   true,
	KindEscalation:  true,
	KindDoneReceipt: true,
}

// MutationOp enumerates §1.3's mutation-log operations.
type MutationOp string

const (
	OpAdd    MutationOp = "add"
	OpUpdate MutationOp = "update"
	OpRemove MutationOp = "remove"
)

// StorageKind enumerates §1.3's before/after payload storage discriminator.
type StorageKind string

const (
	StorageInline     StorageKind = "inline"
	StorageContentRef StorageKind = "content_ref"
)

// SnapshotTrigger enumerates §1.1/§1.2's snapshot triggers.
type SnapshotTrigger string

const (
	TriggerPreRollup        SnapshotTrigger = "pre_rollup"
	TriggerScheduledCadence SnapshotTrigger = "scheduled_cadence"
	TriggerManualPreRestore SnapshotTrigger = "manual_pre_restore"
)

// ReceiptKind enumerates the two receipt shapes §3/§7.5/§1.2 share: a
// rollup, a restore-reactivation, and a snapshot-prune.
type ReceiptKind string

const (
	ReceiptRollup              ReceiptKind = "rollup"
	ReceiptRestoreReactivation ReceiptKind = "restore_reactivation"
	ReceiptSnapshotPrune       ReceiptKind = "snapshot_prune"
)

// Record is one materialized current-state entry.
type Record struct {
	WorkspaceID string
	RecordID    string
	RecordKind  RecordKind
	Body        map[string]any
	SHA256      string
	UpdatedAt   string
}

// PayloadSide is §1.3's before/after shape: either nil (add has no
// before, remove has no after), or a fully-resolved storage descriptor.
type PayloadSide struct {
	Storage StorageKind
	SHA256  string
	Body    map[string]any // present iff Storage == StorageInline
	Ref     string         // present iff Storage == StorageContentRef
}

// Mutation is one append-only mutation-log entry (§1.3).
type Mutation struct {
	MutationID   int64
	WorkspaceID  string
	Timestamp    string
	Op           MutationOp
	RecordID     string
	RecordKind   RecordKind
	Before       *PayloadSide
	After        *PayloadSide
	CompactionID string // "" iff not remove/not correlated to a rollup
	RestoreID    string // "" unless this add is a restore_reactivation
	FencingToken int64
}

// Snapshot is a full, self-contained current-state copy (§1.1).
type Snapshot struct {
	SnapshotID           string
	WorkspaceID          string
	Timestamp            string
	Trigger              SnapshotTrigger
	Content              map[string]map[string]map[string]any // kind -> record_id -> body
	ByteCount            int
	SHA256               string
	RecordCount          int
	RecordIDSetSHA256    string
	MutationLogWatermark *int64
	FencingToken         int64
}

// StateSummary is the pre_state/post_state shape shared by §3's compaction
// receipt.
type StateSummary struct {
	SnapshotID        string
	ByteCount         int
	SHA256            string
	RecordCount       int
	RecordIDSetSHA256 string
}

// RolledRecord is one entry of §3's rolled_records list.
type RolledRecord struct {
	RecordID   string
	Kind       RecordKind
	ResolvedAt string
	MutationID int64
}

// ArchiveAppend is §3's archive_append shape.
type ArchiveAppend struct {
	ArchivePath             string
	ByteCountAppended       int
	SHA256OfAppendedBytes   string
	RolledRecordIDSetSHA256 string
}

// CompactionReceipt is §3's compaction receipt (also reused, per §1.2/§7.5,
// for snapshot_prune and restore_reactivation receipts sharing the same
// hash-anchored shape).
type CompactionReceipt struct {
	CompactionID  string
	WorkspaceID   string
	Timestamp     string
	Kind          ReceiptKind
	RestoreID     string
	PreState      StateSummary
	RolledRecords []RolledRecord
	PostState     StateSummary
	ArchiveAppend ArchiveAppend
	FencingToken  int64
	// Phase tracks the §5 crash/retry state machine: "archived" once (c) is
	// durably confirmed, "committed" once (d)'s current-state swap has
	// also completed. A receipt stuck at "archived" after a crash is what
	// ResumeCompactions detects and finishes.
	Phase string
}
