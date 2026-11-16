package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"gorm.io/gorm"
)

// Hand-written SQL goes through GORM's Raw/Exec with PostgreSQL $n
// placeholders: GORM passes the statement through unchanged and appends the
// arguments in order, so the pgx driver encodes them natively (text[] for
// []string, jsonb for maps and structs). Statements that GORM's builder can
// express are written with the builder instead.

// errRawSyntax guards the pass-through: GORM rewrites '?' and '@name' when arguments are given.
var errRawSyntax = errors.New("store: raw SQL with arguments must use $n placeholders only (no '?' or '@')")

func rawSQLOK(sqlText string, args []any) bool {
	return len(args) == 0 || !strings.ContainsAny(sqlText, "?@")
}

// Raw prepares a hand-written query on q.
func Raw(ctx context.Context, q DB, sqlText string, args ...any) *gorm.DB {
	g := q.Gorm(ctx)
	if !rawSQLOK(sqlText, args) {
		_ = g.AddError(errRawSyntax)
		return g
	}
	return g.Raw(sqlText, args...)
}

// Result is what a statement changed.
type Result struct{ n int64 }

// RowsAffected is the number of rows the statement changed.
func (r Result) RowsAffected() int64 { return r.n }

// Exec runs a hand-written statement.
func Exec(ctx context.Context, q DB, sqlText string, args ...any) (Result, error) {
	if !rawSQLOK(sqlText, args) {
		return Result{}, errRawSyntax
	}
	res := q.Gorm(ctx).Exec(sqlText, args...)
	return Result{res.RowsAffected}, res.Error
}

// Affected returns ErrNotFound when the statement changed no row.
func Affected(r Result, err error) error {
	if err != nil {
		return MapErr(err)
	}
	if r.n == 0 {
		return ErrNotFound
	}
	return nil
}

func affected(r Result, err error) error { return Affected(r, err) }

// Scanner is one row (Row or Rows).
type Scanner interface{ Scan(dest ...any) error }

// Row is the single row of QueryRow; Scan reports sql.ErrNoRows when there is none.
type Row struct {
	row *sql.Row
	err error
}

// Scan copies the columns into dest (PostgreSQL arrays and json decode into Go values).
func (r Row) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return r.row.Scan(wrapAll(dest)...)
}

// QueryRow runs a hand-written query that returns at most one row.
func QueryRow(ctx context.Context, q DB, sqlText string, args ...any) Row {
	g := Raw(ctx, q, sqlText, args...)
	if g.Error != nil {
		return Row{err: g.Error}
	}
	return Row{row: g.Row()}
}

// Rows iterates a hand-written query's rows.
type Rows struct{ *sql.Rows }

// Scan copies the current row's columns into dest.
func (r *Rows) Scan(dest ...any) error { return r.Rows.Scan(wrapAll(dest)...) }

// Close releases the rows.
func (r *Rows) Close() { _ = r.Rows.Close() }

// Query runs a hand-written query.
func Query(ctx context.Context, q DB, sqlText string, args ...any) (*Rows, error) {
	g := Raw(ctx, q, sqlText, args...)
	if g.Error != nil {
		return nil, g.Error
	}
	rows, err := g.Rows()
	if err != nil {
		return nil, err
	}
	return &Rows{rows}, nil
}

// Collect scans every row with scan.
func Collect[T any](rows *Rows, err error, scan func(Scanner) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Strings collects a one-column text query.
func Strings(ctx context.Context, q DB, sqlText string, args ...any) ([]string, error) {
	rows, err := Query(ctx, q, sqlText, args...)
	return Collect(rows, err, func(r Scanner) (string, error) {
		var s string
		return s, r.Scan(&s)
	})
}

// Param makes a value safe as a GORM builder argument: GORM expands slices
// into "(a,b,c)" lists, so a slice is handed to the pgx driver whole (text[]
// for []string, jsonb for other slices and maps) and a named byte slice binds as bytes.
func Param(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Map {
		return whole{v}
	}
	if rv.Kind() != reflect.Slice {
		return v
	}
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		return rv.Bytes()
	}
	if s, ok := v.([]string); ok {
		return TextArray(s)
	}
	return whole{v}
}

// whole passes a value to the pgx driver untouched.
type whole struct{ v any }

// Value implements driver.Valuer.
func (w whole) Value() (driver.Value, error) { return w.v, nil }

// TextArray binds a []string as one text[] argument.
type TextArray []string

// Value implements driver.Valuer (pgx encodes the slice as text[]).
func (a TextArray) Value() (driver.Value, error) {
	if a == nil {
		return []string{}, nil
	}
	return []string(a), nil
}

// Scan implements sql.Scanner.
func (a *TextArray) Scan(src any) error {
	var s []string
	err := scanTextArray(src, &s)
	*a = s
	return err
}

var (
	timeType    = reflect.TypeOf(time.Time{})
	scannerType = reflect.TypeOf((*sql.Scanner)(nil)).Elem()
	typeMaps    = sync.Pool{New: func() any { return pgtype.NewMap() }}
)

func wrapAll(dest []any) []any {
	out := make([]any, len(dest))
	for i, d := range dest {
		out[i] = wrapDest(d)
	}
	return out
}

// wrapDest leaves what database/sql scans natively and decodes the rest (arrays, json).
func wrapDest(d any) any {
	if _, ok := d.(sql.Scanner); ok {
		return d
	}
	rv := reflect.ValueOf(d)
	if rv.Kind() != reflect.Pointer || native(rv.Type().Elem()) {
		return d
	}
	return decoded{d}
}

func native(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType || reflect.PointerTo(t).Implements(scannerType) {
		return true
	}
	switch t.Kind() {
	case reflect.Bool, reflect.String, reflect.Float32, reflect.Float64,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	case reflect.Slice:
		return t.Elem().Kind() == reflect.Uint8
	}
	return false
}

// decoded scans a PostgreSQL array (text form "{…}") or json into its destination.
type decoded struct{ dest any }

func (d decoded) Scan(src any) error {
	v := reflect.ValueOf(d.dest).Elem()
	if src == nil {
		v.Set(reflect.Zero(v.Type()))
		return nil
	}
	if v.Kind() == reflect.Interface {
		return scanAny(src, v)
	}
	b := srcBytes(src)
	if v.Kind() == reflect.Slice && len(b) > 0 && b[0] == '{' {
		return scanPGArray(b, d.dest)
	}
	return json.Unmarshal(b, d.dest)
}

// scanAny decodes json into an `any` destination and keeps other values as they come.
func scanAny(src any, v reflect.Value) error {
	if b, ok := src.([]byte); ok {
		var x any
		if json.Unmarshal(b, &x) == nil {
			v.Set(reflect.ValueOf(&x).Elem())
			return nil
		}
		src = string(b)
	}
	v.Set(reflect.ValueOf(src))
	return nil
}

func srcBytes(src any) []byte {
	switch s := src.(type) {
	case []byte:
		return s
	case string:
		return []byte(s)
	}
	b, _ := json.Marshal(src)
	return b
}

func scanPGArray(b []byte, dest any) error {
	m := typeMaps.Get().(*pgtype.Map)
	defer typeMaps.Put(m)
	t, ok := m.TypeForValue(dest)
	if !ok {
		t, _ = m.TypeForOID(pgtype.TextArrayOID)
	}
	return m.Scan(t.OID, pgtype.TextFormatCode, b, dest)
}

func scanTextArray(src any, dest *[]string) error {
	*dest = []string{}
	if src == nil {
		return nil
	}
	b := srcBytes(src)
	if len(b) > 0 && b[0] == '[' {
		return json.Unmarshal(b, dest)
	}
	if err := scanPGArray(b, dest); err != nil {
		return err
	}
	if *dest == nil {
		*dest = []string{}
	}
	return nil
}
