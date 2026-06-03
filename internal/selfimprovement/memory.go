package selfimprovement

import (
	"fmt"
	"strings"

	"github.com/eloylp/agents/internal/store"
)

const (
	MemoryStatusActive   = "active"
	MemoryStatusProposed = "proposed"
	MemoryStatusArchived = "archived"
	MemoryStatusRejected = "rejected"
)

type AssistantMemory struct {
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

type AssistantMemoryInput struct {
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

type AssistantMemoryUpdate struct {
	Key        *string `json:"key,omitempty"`
	Value      *string `json:"value,omitempty"`
	Confidence *string `json:"confidence,omitempty"`
}

func (s *Service) ListMemory(workspace, status string, limit int) ([]AssistantMemory, error) {
	rows, err := s.store.ListAssistantMemory(workspace, status, limit)
	if err != nil {
		return nil, err
	}
	out := make([]AssistantMemory, 0, len(rows))
	for _, row := range rows {
		out = append(out, memoryFromRow(row))
	}
	return out, nil
}

func (s *Service) ListActiveMemory(workspace string, limit int) ([]AssistantMemory, error) {
	return s.ListMemory(workspace, MemoryStatusActive, limit)
}

func (s *Service) CreateMemory(in AssistantMemoryInput) (AssistantMemory, error) {
	in.Key = strings.TrimSpace(in.Key)
	in.Value = strings.TrimSpace(in.Value)
	if in.Status == "" {
		in.Status = MemoryStatusActive
	}
	if err := validateMemoryStatus(in.Status, true); err != nil {
		return AssistantMemory{}, err
	}
	row, err := s.store.CreateAssistantMemory(store.AssistantMemoryInputRow{
		WorkspaceID:  in.WorkspaceID,
		Key:          in.Key,
		Value:        in.Value,
		Status:       in.Status,
		EvidenceType: in.EvidenceType,
		EvidenceID:   in.EvidenceID,
		EvidenceURL:  in.EvidenceURL,
		Confidence:   in.Confidence,
		ProposedBy:   in.ProposedBy,
	})
	return memoryFromRow(row), err
}

func (s *Service) UpdateMemory(id string, in AssistantMemoryUpdate) (AssistantMemory, error) {
	row, err := s.store.UpdateAssistantMemory(id, store.AssistantMemoryUpdateRow{
		Key:        in.Key,
		Value:      in.Value,
		Confidence: in.Confidence,
	})
	return memoryFromRow(row), err
}

func (s *Service) ApproveMemory(id string) (AssistantMemory, error) {
	row, err := s.store.SetAssistantMemoryStatus(id, MemoryStatusActive, "")
	return memoryFromRow(row), err
}

func (s *Service) RejectMemory(id, reason string) (AssistantMemory, error) {
	row, err := s.store.SetAssistantMemoryStatus(id, MemoryStatusRejected, reason)
	return memoryFromRow(row), err
}

func (s *Service) ArchiveMemory(id string) (AssistantMemory, error) {
	row, err := s.store.SetAssistantMemoryStatus(id, MemoryStatusArchived, "")
	return memoryFromRow(row), err
}

func (s *Service) proposeMemoryFromDecision(workspace, evidenceType, evidenceID, evidenceURL, key, value string) error {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return nil
	}
	existing, err := s.ListMemory(workspace, "", 500)
	if err != nil {
		return err
	}
	normalizedKey := strings.EqualFold
	for _, memory := range existing {
		if normalizedKey(memory.Key, key) && memory.Status != MemoryStatusRejected && memory.Status != MemoryStatusArchived {
			return nil
		}
	}
	_, err = s.CreateMemory(AssistantMemoryInput{
		WorkspaceID:  workspace,
		Key:          key,
		Value:        value,
		Status:       MemoryStatusProposed,
		EvidenceType: evidenceType,
		EvidenceID:   evidenceID,
		EvidenceURL:  evidenceURL,
		Confidence:   "low",
		ProposedBy:   "self-improvement",
	})
	return err
}

func validateMemoryStatus(status string, create bool) error {
	switch status {
	case MemoryStatusActive, MemoryStatusProposed:
		return nil
	case MemoryStatusArchived, MemoryStatusRejected:
		if create {
			return &store.ErrValidation{Msg: fmt.Sprintf("unsupported memory status %q", status)}
		}
		return nil
	default:
		return &store.ErrValidation{Msg: fmt.Sprintf("unsupported memory status %q", status)}
	}
}

func memoryFromRow(row store.AssistantMemoryRow) AssistantMemory {
	return AssistantMemory{
		ID:             row.ID,
		WorkspaceID:    row.WorkspaceID,
		Key:            row.Key,
		Value:          row.Value,
		Status:         row.Status,
		EvidenceType:   row.EvidenceType,
		EvidenceID:     row.EvidenceID,
		EvidenceURL:    row.EvidenceURL,
		Confidence:     row.Confidence,
		ProposedBy:     row.ProposedBy,
		RejectedReason: row.RejectedReason,
		CreatedAt:      row.CreatedAt,
		UpdatedAt:      row.UpdatedAt,
		ApprovedAt:     row.ApprovedAt,
		ArchivedAt:     row.ArchivedAt,
	}
}
