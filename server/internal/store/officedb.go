package store

import (
	"context"

	"github.com/touwaeriol/agent-teams/server/internal/office"
)

// OfficeDB adapts the store to office.DB (global layouts in at_office_layouts).
type OfficeDB struct{ S *Store }

// LoadLayouts implements office.DB.
func (o OfficeDB) LoadLayouts(ctx context.Context) ([]office.DBRow, error) {
	rows, err := o.S.OfficeLayouts(ctx)
	out := make([]office.DBRow, len(rows))
	for i, r := range rows {
		out[i] = office.DBRow{ID: r.ID, Doc: r.Doc, UpdatedAt: r.UpdatedAt}
	}
	return out, err
}

// SaveLayout implements office.DB.
func (o OfficeDB) SaveLayout(ctx context.Context, id string, doc []byte, by string) (office.DBRow, error) {
	r, err := o.S.SaveOfficeLayout(ctx, id, doc, by)
	return office.DBRow{ID: r.ID, Doc: r.Doc, UpdatedAt: r.UpdatedAt}, err
}

// DeleteLayout implements office.DB.
func (o OfficeDB) DeleteLayout(ctx context.Context, id string) error {
	err := o.S.DeleteOfficeLayout(ctx, id)
	if err == ErrNotFound {
		return office.ErrNotFound
	}
	return err
}
