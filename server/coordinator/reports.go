package coordinator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	ReportCycle    = "cycle"
	ReportDaily    = "daily"
	ReportStatus   = "status"
	maxReportBytes = 64 * 1024
)

type ReportArtifact struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	CreatedAt     string `json:"created_at"`
	Trigger       string `json:"trigger,omitempty"`
	OccurrenceKey string `json:"occurrence_key,omitempty"`
}

type PublishReportInput struct {
	Type          string         `json:"type"`
	Title         string         `json:"title"`
	Body          string         `json:"body"`
	Trigger       string         `json:"trigger,omitempty"`
	OccurrenceKey string         `json:"occurrence_key,omitempty"`
	State         PublishedState `json:"state"`
}

type ReportPage struct {
	Reports    []ReportArtifact `json:"reports"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

func (p *Plugin) publishReport(ctx context.Context, workspaceID string, input PublishReportInput) (ReportArtifact, CoordinatorState, error) {
	if err := validateReportInput(input); err != nil {
		return ReportArtifact{}, CoordinatorState{}, err
	}
	now := p.now().UTC()
	artifact := ReportArtifact{
		ID: fmt.Sprintf("%d-%s", now.UnixNano(), input.Type), Type: input.Type,
		Title: strings.TrimSpace(input.Title), Body: input.Body, CreatedAt: now.Format(time.RFC3339Nano),
		Trigger: input.Trigger, OccurrenceKey: input.OccurrenceKey,
	}
	doc, err := p.updateDocument(ctx, workspaceID, func(doc *workspaceDocument) error {
		doc.State.ActiveFlags = append([]ActiveFlag(nil), input.State.ActiveFlags...)
		doc.State.TaskSnapshots = input.State.TaskSnapshots
		if doc.State.TaskSnapshots == nil {
			doc.State.TaskSnapshots = map[string]TaskActivitySnapshot{}
		}
		doc.State.Degradations = append([]string(nil), input.State.Degradations...)
		if input.State.CycleLog != nil {
			doc.State.CycleLogs = append([]CycleLog{*input.State.CycleLog}, doc.State.CycleLogs...)
		}
		doc.State.LastReportAt = artifact.CreatedAt
		doc.Reports = append([]ReportArtifact{artifact}, doc.Reports...)
		return nil
	})
	return artifact, doc.State, err
}

func validateReportInput(input PublishReportInput) error {
	if input.Type != ReportCycle && input.Type != ReportDaily && input.Type != ReportStatus {
		return fmt.Errorf("report type must be cycle, daily, or status")
	}
	if strings.TrimSpace(input.Title) == "" || len(input.Title) > 200 {
		return fmt.Errorf("report title must be between 1 and 200 characters")
	}
	if strings.TrimSpace(input.Body) == "" {
		return fmt.Errorf("report body is required")
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if len(encoded) > maxReportBytes {
		return fmt.Errorf("report payload exceeds %d bytes", maxReportBytes)
	}
	return nil
}

func (p *Plugin) listReports(ctx context.Context, workspaceID, cursor string, limit int) (ReportPage, error) {
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return ReportPage{}, fmt.Errorf("report limit must be between 1 and 100")
	}
	offset, err := decodeCursor(cursor)
	if err != nil {
		return ReportPage{}, err
	}
	lock := p.locks.forWorkspace(workspaceID)
	lock.Lock()
	defer lock.Unlock()
	doc, err := p.loadDocument(ctx, workspaceID)
	if err != nil {
		return ReportPage{}, err
	}
	if offset > len(doc.Reports) {
		return ReportPage{}, fmt.Errorf("report cursor is out of range")
	}
	end := offset + limit
	if end > len(doc.Reports) {
		end = len(doc.Reports)
	}
	page := ReportPage{Reports: append([]ReportArtifact(nil), doc.Reports[offset:end]...)}
	if end < len(doc.Reports) {
		page.NextCursor = encodeCursor(end)
	}
	return page, nil
}

func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodeCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("invalid report cursor")
	}
	offset, err := strconv.Atoi(string(decoded))
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid report cursor")
	}
	return offset, nil
}
