// Package sqlutil
package sqlutil

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

type StructSelectOptions struct {
	FieldPrefix     string
	IncludeEmbedded bool
}

type StructSelectTargets struct {
	ScanDest []any
	FieldIDs string
}

type structSelectField struct {
	IndexPath []int
	FieldID   string
}

type structSelectMeta struct {
	Fields []structSelectField
}

var structSelectCache sync.Map // map[reflect.Type]structSelectMeta

func BuildStructSelectTargets(resultStruct any, opts StructSelectOptions) (StructSelectTargets, error) {
	if resultStruct == nil {
		return StructSelectTargets{}, fmt.Errorf("resultStruct is nil")
	}

	v := reflect.ValueOf(resultStruct)
	if v.Kind() != reflect.Pointer {
		return StructSelectTargets{}, fmt.Errorf("resultStruct must be a pointer to a struct")
	}

	if v.IsNil() {
		return StructSelectTargets{}, fmt.Errorf("resultStruct is nil pointer")
	}

	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return StructSelectTargets{}, fmt.Errorf("resultStruct must be a pointer to a struct")
	}

	meta, err := getStructSelectMeta(v.Type(), opts)
	if err != nil {
		return StructSelectTargets{}, err
	}

	targets := StructSelectTargets{
		ScanDest: make([]any, 0, len(meta.Fields)),
	}

	var fieldIDs strings.Builder

	for _, field := range meta.Fields {
		fieldValue, ok := fieldByIndexPath(v, field.IndexPath)
		if !ok {
			continue
		}

		if !fieldValue.CanAddr() || !fieldValue.CanInterface() {
			continue
		}

		targets.ScanDest = append(targets.ScanDest, fieldValue.Addr().Interface())

		if fieldIDs.Len() > 0 {
			fieldIDs.WriteString(",")
		}

		fieldIDs.WriteString(opts.FieldPrefix)
		fieldIDs.WriteString(field.FieldID)
	}

	targets.FieldIDs = fieldIDs.String()

	return targets, nil
}

func getStructSelectMeta(t reflect.Type, opts StructSelectOptions) (structSelectMeta, error) {
	cacheKey := structSelectCacheKey{
		Type:            t,
		IncludeEmbedded: opts.IncludeEmbedded,
	}

	if cached, ok := structSelectCache.Load(cacheKey); ok {
		return cached.(structSelectMeta), nil
	}

	meta := structSelectMeta{
		Fields: make([]structSelectField, 0, t.NumField()),
	}

	if err := collectStructSelectMeta(t, nil, opts, &meta); err != nil {
		return structSelectMeta{}, err
	}

	actual, _ := structSelectCache.LoadOrStore(cacheKey, meta)

	return actual.(structSelectMeta), nil
}

type structSelectCacheKey struct {
	Type            reflect.Type
	IncludeEmbedded bool
}

func collectStructSelectMeta(
	t reflect.Type,
	parentIndex []int,
	opts StructSelectOptions,
	meta *structSelectMeta,
) error {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return nil
	}

	for i := 0; i < t.NumField(); i++ {
		structField := t.Field(i)
		indexPath := appendIndex(parentIndex, i)

		fieldType := structField.Type
		if fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}

		if structField.Anonymous && opts.IncludeEmbedded && fieldType.Kind() == reflect.Struct {
			if err := collectStructSelectMeta(fieldType, indexPath, opts, meta); err != nil {
				return err
			}

			continue
		}

		if structField.PkgPath != "" {
			continue
		}

		fieldID, ok := sqlFieldName(structField)
		if !ok {
			continue
		}

		meta.Fields = append(meta.Fields, structSelectField{
			IndexPath: indexPath,
			FieldID:   fieldID,
		})
	}

	return nil
}

func fieldByIndexPath(v reflect.Value, indexPath []int) (reflect.Value, bool) {
	for _, index := range indexPath {
		for v.Kind() == reflect.Interface || v.Kind() == reflect.Ptr {
			if v.IsNil() {
				if !v.CanSet() {
					return reflect.Value{}, false
				}

				v.Set(reflect.New(v.Type().Elem()))
			}

			v = v.Elem()
		}

		if v.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}

		if index < 0 || index >= v.NumField() {
			return reflect.Value{}, false
		}

		v = v.Field(index)
	}

	return v, true
}

func sqlFieldName(field reflect.StructField) (string, bool) {
	if sqlTag, ok := field.Tag.Lookup("sql"); ok && sqlTag == "false" {
		return "", false
	}

	if fTag, ok := field.Tag.Lookup("f"); ok {
		name := cleanTagName(fTag)
		if name == "" || name == "-" {
			return "", false
		}

		return name, true
	}

	jsonTag, ok := field.Tag.Lookup("json")
	if !ok {
		return "", false
	}

	name := cleanTagName(jsonTag)
	if name == "" || name == "-" {
		return "", false
	}

	return name, true
}

func cleanTagName(tag string) string {
	name, _, _ := strings.Cut(tag, ",")
	return strings.TrimSpace(name)
}

func appendIndex(parent []int, index int) []int {
	out := make([]int, 0, len(parent)+1)
	out = append(out, parent...)
	out = append(out, index)

	return out
}
