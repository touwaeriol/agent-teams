package store

import (
	"context"
	"encoding/json"
	"reflect"

	"gorm.io/gorm/schema"
)

// GORM serializers for the PostgreSQL column types the models use:
//
//	gorm:"serializer:jsonb"  NOT NULL jsonb; nil maps/slices are stored as {} / [] and read back non-nil
//	gorm:"serializer:pgtext" text[]; nil is stored as '{}' and read back as an empty slice
func init() {
	schema.RegisterSerializer("jsonb", jsonbSerializer{})
	schema.RegisterSerializer("pgtext", textArraySerializer{})
}

type jsonbSerializer struct{}

func (jsonbSerializer) Scan(_ context.Context, field *schema.Field, dst reflect.Value, dbValue any) error {
	v := reflect.New(field.FieldType)
	if dbValue != nil {
		if err := json.Unmarshal(srcBytes(dbValue), v.Interface()); err != nil {
			return err
		}
	}
	nonNilValue(v.Elem())
	field.ReflectValueOf(context.Background(), dst).Set(v.Elem())
	return nil
}

func (jsonbSerializer) Value(_ context.Context, _ *schema.Field, _ reflect.Value, fieldValue any) (any, error) {
	v := reflect.ValueOf(fieldValue)
	if v.IsValid() {
		cp := reflect.New(v.Type()).Elem()
		cp.Set(v)
		nonNilValue(cp)
		fieldValue = cp.Interface()
	}
	b, err := json.Marshal(fieldValue)
	return string(b), err
}

// nonNilValue replaces a nil map or slice with an empty one.
func nonNilValue(v reflect.Value) {
	switch {
	case v.Kind() == reflect.Map && v.IsNil():
		v.Set(reflect.MakeMap(v.Type()))
	case v.Kind() == reflect.Slice && v.IsNil():
		v.Set(reflect.MakeSlice(v.Type(), 0, 0))
	}
}

type textArraySerializer struct{}

func (textArraySerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue any) error {
	var out []string
	if err := scanTextArray(dbValue, &out); err != nil {
		return err
	}
	field.ReflectValueOf(ctx, dst).Set(reflect.ValueOf(out))
	return nil
}

func (textArraySerializer) Value(_ context.Context, _ *schema.Field, _ reflect.Value, fieldValue any) (any, error) {
	s, _ := fieldValue.([]string)
	return TextArray(s).Value()
}
