package tools

import (
	"reflect"
	"strings"
)

// SchemaOf derives a JSON-schema-shaped map from a Go struct. It intentionally
// supports the small, portable subset used by function-calling providers.
func SchemaOf(value interface{}) map[string]interface{} {
	typeOf := reflect.TypeOf(value)
	if typeOf == nil {
		return map[string]interface{}{"type": "object"}
	}
	for typeOf.Kind() == reflect.Ptr {
		typeOf = typeOf.Elem()
	}
	return schemaForType(typeOf)
}

func schemaForType(typeOf reflect.Type) map[string]interface{} {
	for typeOf.Kind() == reflect.Ptr {
		typeOf = typeOf.Elem()
	}
	switch typeOf.Kind() {
	case reflect.Struct:
		return schemaForStruct(typeOf)
	case reflect.String:
		return map[string]interface{}{"type": "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]interface{}{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]interface{}{"type": "number"}
	case reflect.Bool:
		return map[string]interface{}{"type": "boolean"}
	case reflect.Slice, reflect.Array:
		return map[string]interface{}{"type": "array", "items": schemaForType(typeOf.Elem())}
	case reflect.Map:
		return map[string]interface{}{"type": "object"}
	default:
		return map[string]interface{}{"type": "string"}
	}
}

func schemaForStruct(typeOf reflect.Type) map[string]interface{} {
	properties := make(map[string]interface{})
	required := make([]string, 0)
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		if !field.IsExported() {
			continue
		}
		name, optional, skip := schemaFieldName(field)
		if skip {
			continue
		}
		property := schemaForType(field.Type)
		if description := firstNonEmpty(field.Tag.Get("desc"), field.Tag.Get("description")); description != "" {
			property["description"] = description
		}
		if enum := splitNonEmpty(field.Tag.Get("enum")); len(enum) > 0 {
			values := make([]interface{}, len(enum))
			for i := range enum {
				values[i] = enum[i]
			}
			property["enum"] = values
		}
		if defaultValue := field.Tag.Get("default"); defaultValue != "" {
			property["default"] = defaultValue
		}
		properties[name] = property
		if !optional {
			required = append(required, name)
		}
	}
	schema := map[string]interface{}{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func schemaFieldName(field reflect.StructField) (name string, optional bool, skip bool) {
	name = field.Name
	parts := strings.Split(field.Tag.Get("json"), ",")
	if parts[0] == "-" {
		return "", false, true
	}
	if parts[0] != "" {
		name = parts[0]
	}
	for _, option := range parts[1:] {
		if option == "omitempty" {
			optional = true
		}
	}
	return name, optional, false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func splitNonEmpty(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func EnumOf(values ...string) map[string]interface{} {
	enum := make([]interface{}, len(values))
	for i := range values {
		enum[i] = values[i]
	}
	return map[string]interface{}{"type": "string", "enum": enum}
}
