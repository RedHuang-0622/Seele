package tool

import (
	"reflect"
	"strings"
)

// SchemaOf 从 Go 结构体自动生成 JSON Schema（map[string]interface{} 格式）。
//
// 设计目的：告别手写 map[string]interface{}，声明一个 struct 即可同时获得：
//   - JSON Schema（供 LLM 理解工具参数）
//   - 编译期类型安全（json.Unmarshal(argsJSON, &input) 直接解析）
//
// 类型映射：
//
//	string        → {"type": "string"}
//	int / int*    → {"type": "integer"}
//	float*        → {"type": "number"}
//	bool          → {"type": "boolean"}
//	[]T           → {"type": "array", "items": {T 的 schema}}
//	struct / *struct → {"type": "object", "properties": {递归展开}}
//
// 标签约定（全部可选）：
//
//	json:"field_name"            → Schema 属性名（默认取 Go 字段名）
//	json:"field_name,omitempty"  → omitempty 的字段不会出现在 required 列表
//	desc:"字段用途说明"            → Schema description（LLM 据此决定传参）
//	enum:"A,B,C"                 → 逗号分隔的枚举值（仅 string 字段有效）
//	default:"值"                  → Schema default 值
//
// 示例：
//
//	type WeatherInput struct {
//	    City string `json:"city" desc:"城市名称"`
//	    Date string `json:"date,omitempty" desc:"日期，格式 YYYY-MM-DD"`
//	}
//
//	schema := SchemaOf(WeatherInput{})
//	// {"type":"object","properties":{"city":{"type":"string","description":"城市名称"},
//	//  "date":{"type":"string","description":"日期，格式 YYYY-MM-DD"}},"required":["city"]}
//
//	engine.RegisterInlineTool("query_weather", "查询天气", schema, handler)
func SchemaOf(v interface{}) map[string]interface{} {
	t := reflect.TypeOf(v)
	if t == nil {
		return map[string]interface{}{"type": "object"}
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return basicTypeSchema(t)
	}
	return structToSchema(t)
}

// structToSchema 递归展开结构体字段。
func structToSchema(t reflect.Type) map[string]interface{} {
	properties := make(map[string]interface{})
	required := make([]string, 0)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}

		name := f.Name
		omitempty := false
		if raw := f.Tag.Get("json"); raw != "" {
			parts := strings.Split(raw, ",")
			if parts[0] == "-" {
				continue
			}
			if parts[0] != "" {
				name = parts[0]
			}
			for _, p := range parts[1:] {
				if strings.TrimSpace(p) == "omitempty" {
					omitempty = true
				}
			}
		}

		desc := f.Tag.Get("desc")
		if desc == "" {
			desc = f.Tag.Get("description")
		}

		prop := typeToSchema(f.Type)
		if desc != "" {
			prop["description"] = desc
		}

		if enumRaw := f.Tag.Get("enum"); enumRaw != "" {
			parts := strings.Split(enumRaw, ",")
			enum := make([]interface{}, 0, len(parts))
			for _, p := range parts {
				if v := strings.TrimSpace(p); v != "" {
					enum = append(enum, v)
				}
			}
			if len(enum) > 0 {
				prop["enum"] = enum
			}
		}

		if def := f.Tag.Get("default"); def != "" {
			prop["default"] = def
		}

		properties[name] = prop

		if !omitempty {
			required = append(required, name)
		}
	}

	schema := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// typeToSchema 将 Go 类型映射为 JSON Schema 类型字典。
func typeToSchema(t reflect.Type) map[string]interface{} {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	switch t.Kind() {
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
		return map[string]interface{}{
			"type":  "array",
			"items": typeToSchema(t.Elem()),
		}

	case reflect.Map:
		return map[string]interface{}{"type": "object"}

	case reflect.Struct:
		return structToSchema(t)

	default:
		return map[string]interface{}{"type": "string"}
	}
}

func basicTypeSchema(t reflect.Type) map[string]interface{} {
	return typeToSchema(t)
}

// EnumOf 生成带 enum 约束的 string schema。
//
//	EnumOf("uppercase", "lowercase")  →  {"type":"string","enum":["uppercase","lowercase"]}
func EnumOf(values ...string) map[string]interface{} {
	enum := make([]interface{}, len(values))
	for i, v := range values {
		enum[i] = v
	}
	return map[string]interface{}{
		"type": "string",
		"enum": enum,
	}
}
