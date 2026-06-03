package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type AssistantMemoryRow struct {
	ID             string `json:"id"`
	WorkspaceID    string `json:"workspace"`
	Key            string `json:"key"`
	Value          string `json:"value"`
	Status         string `json:"status"`
	EvidenceType   string `json:"evidence_type"`
	EvidenceID     string `json:"evidence_id,omitempty"`
	EvidenceURL    string `json:"evidence_url,omitempty"`
	Confidence     string `json:"confidence"`
	ProposedBy     string `json:"proposed_by,omitempty"`
	RejectedReason string `json:"rejected_reason,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	ApprovedAt     string `json:"approved_at,omitempty"`
	ArchivedAt     string `json:"archived_at,omitempty"`
}

type AssistantMemoryInputRow struct {
	WorkspaceID  string
	Key          string
	Value        string
	Status       string
	EvidenceType string
	EvidenceID   string
	EvidenceURL  string
	Confidence   string
	ProposedBy   string
}

type AssistantMemoryUpdateRow struct {
	Key        *string
	Value      *string
	Confidence *string
}

func (s *Store) ListAssistantMemory(workspace, status string, limit int) ([]AssistantMemoryRow, error) {
	return ListAssistantMemory(s.db, workspace, status, limit)
}

func (s *Store) GetAssistantMemory(id string) (AssistantMemoryRow, error) {
	return GetAssistantMemory(s.db, id)
}

func (s *Store) CreateAssistantMemory(in AssistantMemoryInputRow) (AssistantMemoryRow, error) {
	return CreateAssistantMemory(s.db, in)
}

func (s *Store) UpdateAssistantMemory(id string, in AssistantMemoryUpdateRow) (AssistantMemoryRow, error) {
	return UpdateAssistantMemory(s.db, id, in)
}

func (s *Store) SetAssistantMemoryStatus(id, status, reason string) (AssistantMemoryRow, error) {
	return SetAssistantMemoryStatus(s.db, id, status, reason)
}

func ListAssistantMemory(db *sql.DB, workspace, status string, limit int) ([]AssistantMemoryRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	status = strings.TrimSpace(status)
	var rows *sql.Rows
	var err error
	if status == "" {
		rows, err = db.Query(assistantMemorySelectSQL()+` ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	} else {
		rows, err = db.Query(assistantMemorySelectSQL()+` WHERE status=? ORDER BY updated_at DESC, id DESC LIMIT ?`, status, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssistantMemoryRow{}
	for rows.Next() {
		row, err := scanAssistantMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func GetAssistantMemory(db *sql.DB, id string) (AssistantMemoryRow, error) {
	row, err := scanAssistantMemory(db.QueryRow(assistantMemorySelectSQL()+` WHERE id=?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return AssistantMemoryRow{}, &ErrNotFound{Msg: fmt.Sprintf("assistant memory %q not found", id)}
	}
	return row, err
}

func CreateAssistantMemory(db *sql.DB, in AssistantMemoryInputRow) (AssistantMemoryRow, error) {
	key := strings.TrimSpace(in.Key)
	value := strings.TrimSpace(in.Value)
	if key == "" {
		return AssistantMemoryRow{}, &ErrValidation{Msg: "memory key is required"}
	}
	if value == "" {
		return AssistantMemoryRow{}, &ErrValidation{Msg: "memory value is required"}
	}
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "proposed" {
		return AssistantMemoryRow{}, &ErrValidation{Msg: fmt.Sprintf("unsupported memory status %q", status)}
	}
	evidenceType := strings.TrimSpace(in.EvidenceType)
	if evidenceType == "" {
		evidenceType = "manual_user_entry"
	}
	confidence := strings.TrimSpace(in.Confidence)
	if confidence == "" {
		confidence = "medium"
	}
	approvedExpr := "datetime('now')"
	if status == "proposed" {
		approvedExpr = "NULL"
	}
	var id string
	err := db.QueryRow(
		`INSERT INTO assistant_memory (
			id, key, value, status, evidence_type, evidence_id, evidence_url,
			confidence, proposed_by, approved_at
		) VALUES ('mem_' || lower(hex(randomblob(16))),?,?,?,?,?,?,?,?,`+approvedExpr+`) RETURNING id`,
		key, value, status, evidenceType, strings.TrimSpace(in.EvidenceID),
		strings.TrimSpace(in.EvidenceURL), confidence, strings.TrimSpace(in.ProposedBy),
	).Scan(&id)
	if err != nil {
		return AssistantMemoryRow{}, err
	}
	return GetAssistantMemory(db, id)
}

func UpdateAssistantMemory(db *sql.DB, id string, in AssistantMemoryUpdateRow) (AssistantMemoryRow, error) {
	current, err := GetAssistantMemory(db, id)
	if err != nil {
		return AssistantMemoryRow{}, err
	}
	key := current.Key
	value := current.Value
	confidence := current.Confidence
	if in.Key != nil {
		key = strings.TrimSpace(*in.Key)
	}
	if in.Value != nil {
		value = strings.TrimSpace(*in.Value)
	}
	if in.Confidence != nil {
		confidence = strings.TrimSpace(*in.Confidence)
	}
	if key == "" {
		return AssistantMemoryRow{}, &ErrValidation{Msg: "memory key is required"}
	}
	if value == "" {
		return AssistantMemoryRow{}, &ErrValidation{Msg: "memory value is required"}
	}
	if confidence == "" {
		confidence = "medium"
	}
	_, err = db.Exec(`UPDATE assistant_memory SET key=?, value=?, confidence=?, updated_at=datetime('now') WHERE id=?`, key, value, confidence, strings.TrimSpace(id))
	if err != nil {
		return AssistantMemoryRow{}, err
	}
	return GetAssistantMemory(db, id)
}

func SetAssistantMemoryStatus(db *sql.DB, id, status, reason string) (AssistantMemoryRow, error) {
	status = strings.TrimSpace(status)
	switch status {
	case "active":
		_, err := db.Exec(`UPDATE assistant_memory SET status='active', approved_at=COALESCE(approved_at, datetime('now')), archived_at=NULL, rejected_reason='', updated_at=datetime('now') WHERE id=?`, strings.TrimSpace(id))
		if err != nil {
			return AssistantMemoryRow{}, err
		}
	case "archived":
		_, err := db.Exec(`UPDATE assistant_memory SET status='archived', archived_at=datetime('now'), updated_at=datetime('now') WHERE id=?`, strings.TrimSpace(id))
		if err != nil {
			return AssistantMemoryRow{}, err
		}
	case "rejected":
		_, err := db.Exec(`UPDATE assistant_memory SET status='rejected', rejected_reason=?, updated_at=datetime('now') WHERE id=?`, strings.TrimSpace(reason), strings.TrimSpace(id))
		if err != nil {
			return AssistantMemoryRow{}, err
		}
	default:
		return AssistantMemoryRow{}, &ErrValidation{Msg: fmt.Sprintf("unsupported memory status %q", status)}
	}
	return GetAssistantMemory(db, id)
}

func assistantMemorySelectSQL() string {
	return `SELECT id, '' AS workspace_id, key, value, status, evidence_type, evidence_id, evidence_url,
		confidence, proposed_by, rejected_reason, created_at, updated_at,
		COALESCE(approved_at, ''), COALESCE(archived_at, '')
		FROM assistant_memory`
}

type assistantMemoryScanner interface {
	Scan(dest ...any) error
}

func scanAssistantMemory(scanner assistantMemoryScanner) (AssistantMemoryRow, error) {
	var row AssistantMemoryRow
	err := scanner.Scan(&row.ID, &row.WorkspaceID, &row.Key, &row.Value, &row.Status,
		&row.EvidenceType, &row.EvidenceID, &row.EvidenceURL, &row.Confidence,
		&row.ProposedBy, &row.RejectedReason, &row.CreatedAt, &row.UpdatedAt,
		&row.ApprovedAt, &row.ArchivedAt)
	return row, err
}
