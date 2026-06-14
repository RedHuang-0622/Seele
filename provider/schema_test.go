package provider

import (
	"encoding/json"
	"testing"
)

// =============================================================================
// SchemaOf 单元测试
// =============================================================================

type SimpleInput struct {
	Name string `json:"name" desc:"用户姓名"`
	Age  int    `json:"age" desc:"用户年龄"`
}

func TestSchemaOf_SimpleStruct(t *testing.T) {
	schema := SchemaOf(SimpleInput{})

	if schema["type"] != "object" {
		t.Fatalf("expected type=object, got %v", schema["type"])
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("properties not a map")
	}

	nameProp := props["name"].(map[string]interface{})
	if nameProp["type"] != "string" {
		t.Errorf("name.type: expected string, got %v", nameProp["type"])
	}
	if nameProp["description"] != "用户姓名" {
		t.Errorf("name.description: expected '用户姓名', got %v", nameProp["description"])
	}

	ageProp := props["age"].(map[string]interface{})
	if ageProp["type"] != "integer" {
		t.Errorf("age.type: expected integer, got %v", ageProp["type"])
	}
	if ageProp["description"] != "用户年龄" {
		t.Errorf("age.description: expected '用户年龄', got %v", ageProp["description"])
	}

	// required 应包含两个字段（都没有 omitempty）
	required := schema["required"].([]string)
	found := make(map[string]bool)
	for _, r := range required {
		found[r] = true
	}
	if !found["name"] || !found["age"] {
		t.Errorf("required: expected [name age], got %v", required)
	}
}

type OmitemptyInput struct {
	Name    string `json:"name"`
	Comment string `json:"comment,omitempty" desc:"备注"`
}

func TestSchemaOf_Omitempty(t *testing.T) {
	schema := SchemaOf(OmitemptyInput{})

	required := schema["required"].([]string)
	for _, r := range required {
		if r == "comment" {
			t.Errorf("comment should NOT be in required (has omitempty)")
		}
	}
	if len(required) != 1 || required[0] != "name" {
		t.Errorf("expected required=[name], got %v", required)
	}
}

type EnumInput struct {
	Op string `json:"op" desc:"操作" enum:"add,delete,update"`
}

func TestSchemaOf_EnumTag(t *testing.T) {
	schema := SchemaOf(EnumInput{})

	props := schema["properties"].(map[string]interface{})
	opProp := props["op"].(map[string]interface{})

	enum, ok := opProp["enum"].([]interface{})
	if !ok {
		t.Fatal("enum not present or wrong type")
	}
	if len(enum) != 3 {
		t.Fatalf("expected 3 enum values, got %d: %v", len(enum), enum)
	}
	expected := []string{"add", "delete", "update"}
	for i, v := range enum {
		if v.(string) != expected[i] {
			t.Errorf("enum[%d]: expected %q, got %q", i, expected[i], v)
		}
	}
}

type DefaultInput struct {
	Count int `json:"count" default:"10"`
}

func TestSchemaOf_DefaultTag(t *testing.T) {
	schema := SchemaOf(DefaultInput{})

	props := schema["properties"].(map[string]interface{})
	countProp := props["count"].(map[string]interface{})

	if countProp["default"] != "10" {
		t.Errorf("expected default=10, got %v", countProp["default"])
	}
}

type NestedInput struct {
	User struct {
		Name string `json:"name" desc:"姓名"`
	} `json:"user"`
	Tags []string `json:"tags" desc:"标签列表"`
}

func TestSchemaOf_NestedStruct(t *testing.T) {
	schema := SchemaOf(NestedInput{})

	props := schema["properties"].(map[string]interface{})

	// 嵌套 struct
	userProp := props["user"].(map[string]interface{})
	if userProp["type"] != "object" {
		t.Errorf("user.type: expected object, got %v", userProp["type"])
	}
	userProps := userProp["properties"].(map[string]interface{})
	userName := userProps["name"].(map[string]interface{})
	if userName["type"] != "string" {
		t.Errorf("user.name.type: expected string, got %v", userName["type"])
	}
	if userName["description"] != "姓名" {
		t.Errorf("user.name.description: expected '姓名', got %v", userName["description"])
	}

	// []string → array
	tagsProp := props["tags"].(map[string]interface{})
	if tagsProp["type"] != "array" {
		t.Errorf("tags.type: expected array, got %v", tagsProp["type"])
	}
	items := tagsProp["items"].(map[string]interface{})
	if items["type"] != "string" {
		t.Errorf("tags.items.type: expected string, got %v", items["type"])
	}
}

// 指针类型应正确处理
type PointerInput struct {
	Name *string `json:"name"`
}

func TestSchemaOf_PointerType(t *testing.T) {
	schema := SchemaOf(PointerInput{})

	props := schema["properties"].(map[string]interface{})
	nameProp := props["name"].(map[string]interface{})
	if nameProp["type"] != "string" {
		t.Errorf("pointer name.type: expected string, got %v", nameProp["type"])
	}
}

// json:"-" 应跳过
type SkipInput struct {
	Internal string `json:"-"`
	Public   string `json:"public"`
}

func TestSchemaOf_SkipField(t *testing.T) {
	schema := SchemaOf(SkipInput{})

	props := schema["properties"].(map[string]interface{})
	if _, ok := props["internal"]; ok {
		t.Error("internal field should be skipped (json:\"-\")")
	}
	if _, ok := props["public"]; !ok {
		t.Error("public field should be present")
	}
}

// ── 完整性：JSON 序列化验证 ────────────────────────────────────────

func TestSchemaOf_ValidJSON(t *testing.T) {
	schema := SchemaOf(SimpleInput{})

	// 确保能序列化为合法 JSON
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("schema not round-trippable: %v", err)
	}

	t.Logf("SimpleInput schema: %s", string(data))
}

// ── EnumOf ──────────────────────────────────────────────────────────

func TestEnumOf(t *testing.T) {
	schema := EnumOf("red", "green", "blue")

	if schema["type"] != "string" {
		t.Errorf("expected type=string, got %v", schema["type"])
	}

	enum := schema["enum"].([]interface{})
	if len(enum) != 3 {
		t.Fatalf("expected 3 enum values, got %d", len(enum))
	}
}

// ── 空结构体 ────────────────────────────────────────────────────────

type EmptyInput struct{}

func TestSchemaOf_EmptyStruct(t *testing.T) {
	schema := SchemaOf(EmptyInput{})

	if schema["type"] != "object" {
		t.Errorf("expected type=object, got %v", schema["type"])
	}

	props := schema["properties"].(map[string]interface{})
	if len(props) != 0 {
		t.Errorf("expected 0 properties, got %d", len(props))
	}
}
