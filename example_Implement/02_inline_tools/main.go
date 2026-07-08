// 02_inline_tools/main.go
//
// RegisterTool 深度演示：声明 struct → SchemaOf 自动生成 JSON Schema。
//
// v0.3 新增 SchemaOf()：告别手写 map[string]interface{}。
//   声明一个 struct + json/desc/enum/default 标签 = 同时获得：
//     - 编译期类型安全（json.Unmarshal(argsJSON, &input)）
//     - JSON Schema 自动生成（供 LLM 理解参数）
//
// 架构：
//
//	agent.New → engine.New → eng.Chat()
//
// 运行前：
//   1. 编辑 ../config/config.yaml，填入你的 LLM API Key
//   2. go run .

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/agent/core/tool"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/types"
)

// =============================================================================
// 工具 1：天气查询
// =============================================================================

type WeatherInput struct {
	City string `json:"city" desc:"城市名称，如 北京、上海、Tokyo"`
	Date string `json:"date,omitempty" desc:"日期，格式 YYYY-MM-DD，默认今天" default:"today"`
}

type WeatherOutput struct {
	City        string  `json:"city"`
	Temperature float64 `json:"temperature"`
	Condition   string  `json:"condition"`
	Humidity    int     `json:"humidity"`
}

func weatherHandler(ctx context.Context, argsJSON string) (string, error) {
	var input WeatherInput
	if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
		return "", fmt.Errorf("weather: 参数解析失败: %w", err)
	}
	if input.City == "" {
		return "", fmt.Errorf("weather: city 参数不能为空")
	}
	result := WeatherOutput{City: input.City, Temperature: 22.5, Condition: "晴朗", Humidity: 55}
	b, _ := json.Marshal(result)
	return string(b), nil
}

// =============================================================================
// 工具 2：文本处理
// =============================================================================

type TextInput struct {
	Text      string `json:"text" desc:"要处理的文本"`
	Operation string `json:"operation" desc:"操作类型" enum:"uppercase,lowercase,count,reverse"`
}

func textHandler(ctx context.Context, argsJSON string) (string, error) {
	var input TextInput
	if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
		return "", fmt.Errorf("text: 参数解析失败: %w", err)
	}
	var result string
	switch input.Operation {
	case "uppercase":
		result = strings.ToUpper(input.Text)
	case "lowercase":
		result = strings.ToLower(input.Text)
	case "count":
		result = fmt.Sprintf("字符数: %d, 单词数: %d", len([]rune(input.Text)), len(strings.Fields(input.Text)))
	case "reverse":
		runes := []rune(input.Text)
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		result = string(runes)
	default:
		return "", fmt.Errorf("text: 不支持的 operation: %s（支持 uppercase/lowercase/count/reverse）", input.Operation)
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

// =============================================================================
// 工具 3：嵌套结构体
// =============================================================================

type Person struct {
	Name string `json:"name" desc:"姓名"`
	Age  int    `json:"age" desc:"年龄"`
}

type TeamInput struct {
	Leader   Person   `json:"leader" desc:"团队负责人"`
	Members  []Person `json:"members" desc:"团队成员列表"`
	TeamName string   `json:"team_name" desc:"团队名称"`
}

func teamHandler(ctx context.Context, argsJSON string) (string, error) {
	var input TeamInput
	if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
		return "", fmt.Errorf("team: 参数解析失败: %w", err)
	}
	b, _ := json.Marshal(map[string]interface{}{
		"team":   input.TeamName,
		"leader": input.Leader.Name,
		"size":   len(input.Members) + 1,
	})
	return string(b), nil
}

// =============================================================================
// 工具 4：带状态的计数器（无参数工具）
// =============================================================================

type counterTool struct {
	mu    sync.Mutex
	count map[string]int
}

type CounterInput struct{}

func (c *counterTool) handler(ctx context.Context, argsJSON string) (string, error) {
	c.mu.Lock()
	c.count["counter"]++
	n := c.count["counter"]
	c.mu.Unlock()
	return fmt.Sprintf(`"计数器已被调用 %d 次"`, n), nil
}

// =============================================================================
// main
// =============================================================================

var configPath = flag.String("c", "../../config/account-anthropic.yaml", "config path (account-anthropic.yaml / account-anthropic.yaml)")

func main() {
	ctx := context.Background()
	flag.Parse()

	result, err := api.LoadFullAccountsConfig(*configPath)
	if err != nil {
		log.Fatalf("load config %s: %v", *configPath, err)
	}
	ls := result.LLMDefaults
	pool := result.Pool
	first := pool.All()[0]

	llmCfg := types.LLMConfig{
		BaseURL:     first.BaseURL,
		APIKey:      first.APIKey,
		Model:       first.Model,
		MaxTokens:   ls.MaxTokens,
		Timeout:     ls.Timeout,
		Temperature: ls.Temperature,
	}
	agt, err := agent.New(agent.Options{
		LLMConfig:       llmCfg,
		HubStartupDelay: 10,
	})
	if err != nil {
		log.Fatalf("agent init failed: %v", err)
	}
	defer agt.Shutdown()

	chatClient := agt.LLM().(*api.ChatClient)
	chatClient.WithAccountPool(pool)
	if ls.Provider != "" {
		chatClient.SetProvider(ls.Provider)
	}

	// ── 注册工具 ─────────────────────────────────────────────────────

	agt.RegisterTool(
		"query_weather",
		"查询指定城市的天气信息，返回温度、天气状况和湿度",
		tool.SchemaOf(WeatherInput{}),
		weatherHandler,
	)

	agt.RegisterTool(
		"process_text",
		"对文本执行操作：大写(uppercase)、小写(lowercase)、字数统计(count)、反转(reverse)",
		tool.SchemaOf(TextInput{}),
		textHandler,
	)

	agt.RegisterTool(
		"create_team",
		"创建团队并指定负责人和成员",
		tool.SchemaOf(TeamInput{}),
		teamHandler,
	)

	counter := &counterTool{count: make(map[string]int)}
	agt.RegisterTool(
		"counter",
		"记录并返回工具被调用的总次数，每次调用计数 +1",
		tool.SchemaOf(CounterInput{}),
		counter.handler,
	)

	// ── 打印工具 Schema ──────────────────────────────────────────────
	fmt.Println("=== 自动生成的 JSON Schema ===")
	for _, t := range agt.Tools().Tools() {
		schemaJSON, _ := json.MarshalIndent(t.Function.Parameters, "", "  ")
		fmt.Printf("\n--- %s ---\n%s\n", t.Function.Name, string(schemaJSON))
	}

	// ── Engine：注入 Agent，内部接管 contexts ────────────────────────
	eng := engine.New(agt, engine.WithSystemPrompt("你是一个全能助手，可以查天气、处理文本、创建团队。"))

	// 多轮对话（engine 内部维护 history）
	reply, _ := eng.Chat(ctx, "北京今天天气怎么样？")
	fmt.Println("\n🌤 天气:", reply)

	reply, _ = eng.Chat(ctx, "把 Shanghai 的天气用大写字母写出来")
	fmt.Println("📝 文本:", reply)

	reply, _ = eng.Chat(ctx, "创建一个名为 Avengers 的团队，Leader 是 Tony 40岁，成员有 Steve 100岁 和 Thor 1500岁")
	fmt.Println("👥 团队:", reply)

	for i := 0; i < 3; i++ {
		reply, _ = eng.Chat(ctx, "调用一次计数器")
	}
	fmt.Println("🔢 计数器:", reply)

	// ── 总结 ────────────────────────────────────────────────────────
	fmt.Println("\n=== SchemaOf 支持的全部标签 ===")
	fmt.Println("  json:\"name\"               → 属性名")
	fmt.Println("  json:\"name,omitempty\"      → 非必填字段")
	fmt.Println("  desc:\"说明文字\"             → description（LLM 据此传参）")
	fmt.Println("  enum:\"A,B,C\"               → 枚举约束（string 字段）")
	fmt.Println("  default:\"值\"               → 默认值")
	fmt.Println()
	fmt.Println("  嵌套 struct / []struct → 自动递归展开")
	fmt.Println("  int/float/bool          → 自动映射 integer/number/boolean")

	_ = time.Second
}
