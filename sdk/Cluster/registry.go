package cluster

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ── 个性注册表（registries/registry_*.yaml）─────────────────────

type AgentRegistry struct {
	Role struct {
		Name         string `yaml:"name"`
		Display      string `yaml:"display"`
		Tier         string `yaml:"tier"` // scanner | responder | coordinator
		SystemPrompt string `yaml:"system_prompt"`
		Workflow     string `yaml:"workflow"`
	} `yaml:"role"`

	Provides struct {
		Capabilities []AgentCapability `yaml:"capabilities"`
	} `yaml:"provides"`

	Uses struct {
		Tools []string `yaml:"tools"`
	} `yaml:"uses"`

	Peers []struct {
		Name         string   `yaml:"name"`
		Capabilities []string `yaml:"capabilities"`
	} `yaml:"peers"`

	Network struct {
		Port int `yaml:"port"`
	} `yaml:"network"`
}

type AgentCapability struct {
	Name         string `yaml:"name"`
	Description  string `yaml:"description"`
	InputSchema  string `yaml:"input_schema"`
	OutputSchema string `yaml:"output_schema"`
}

// ── 注册表文档（主注册表 + 工具地址簿共用此结构）───────────────

type registryDoc struct {
	Services struct {
		Tools []registryTool `yaml:"tools"`
		Hubs  []registryHub  `yaml:"hubs"`
	} `yaml:"services"`
	Pool map[string]interface{} `yaml:"pool"`
}

type registryTool struct {
	Name         string `yaml:"name"`
	Addr         string `yaml:"addr"`
	Method       string `yaml:"method"`
	Description  string `yaml:"description"`
	InputSchema  string `yaml:"input_schema"`
	OutputSchema string `yaml:"output_schema"`
}

type registryHub struct {
	Name string `yaml:"name"`
	Addr string `yaml:"addr"`
}

// ── 注册表合并 ──────────────────────────────────────────────────

// BuildAgentRegistry 根据个性注册表中的 uses.tools 和 peers，
// 从工具地址簿和主注册表裁剪出本 Agent 需要的工具集，
// 合并写入临时文件，返回临时文件路径。
//
// 流程：
//  1. 从 toolsRegistryPath（registry.tools.yaml）按 uses.tools 筛原子工具
//  2. 从 hubRegistryPath（registry.yaml）按 peers capability 筛 Agent 路由
//  3. 连接池参数优先取 toolsRegistry，fallback 到 hubRegistry
//  4. 写入 /tmp/seele-agent-{role}.yaml
func BuildAgentRegistry(reg *AgentRegistry, hubRegistryPath, toolsRegistryPath string) (string, error) {
	toolSet := make(map[string]bool, len(reg.Uses.Tools))
	for _, t := range reg.Uses.Tools {
		toolSet[t] = true
	}

	peerCapSet := make(map[string]bool)
	for _, p := range reg.Peers {
		for _, c := range p.Capabilities {
			peerCapSet[c] = true
		}
	}

	merged := &registryDoc{}

	// 1. 原子工具：从 registry.tools.yaml 筛选
	if toolsRegistryPath != "" {
		toolsDoc, err := loadRegistryDoc(toolsRegistryPath)
		if err != nil {
			return "", fmt.Errorf("read tools registry %q: %w", toolsRegistryPath, err)
		}
		for _, t := range toolsDoc.Services.Tools {
			if toolSet[t.Name] {
				merged.Services.Tools = append(merged.Services.Tools, t)
			}
		}
		merged.Services.Hubs = toolsDoc.Services.Hubs
		merged.Pool = toolsDoc.Pool
	}

	// 2. Hub registry：筛选 peer capability + fallback pool/hubs
	if hubRegistryPath != "" {
		hubDoc, err := loadRegistryDoc(hubRegistryPath)
		if err != nil {
			return "", fmt.Errorf("read hub registry %q: %w", hubRegistryPath, err)
		}
		if len(peerCapSet) > 0 {
			for _, t := range hubDoc.Services.Tools {
				if peerCapSet[t.Name] {
					merged.Services.Tools = append(merged.Services.Tools, t)
				}
			}
		}
		if len(merged.Services.Hubs) == 0 {
			merged.Services.Hubs = hubDoc.Services.Hubs
		}
		if merged.Pool == nil {
			merged.Pool = hubDoc.Pool
		}
	}

	// 3. 序列化写入临时文件
	out, err := yaml.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("marshal merged registry: %w", err)
	}
	tmpPath := filepath.Join(os.TempDir(), fmt.Sprintf("seele-agent-%s.yaml", reg.Role.Name))
	if err := os.WriteFile(tmpPath, out, 0644); err != nil {
		return "", fmt.Errorf("write merged registry: %w", err)
	}
	return tmpPath, nil
}

func loadRegistryDoc(path string) (*registryDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc := &registryDoc{}
	if err := yaml.Unmarshal(data, doc); err != nil {
		return nil, err
	}
	return doc, nil
}
