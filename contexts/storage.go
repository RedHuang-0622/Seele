package seelectx

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// LocalStorage 是会话数据的持久化接口。
type LocalStorage interface {
	// Save 以 key 保存数据。
	Save(key string, data interface{}) error
	// Load 加载 key 对应的数据到 target 中。
	Load(key string, target interface{}) error
	// List 返回所有 key 列表。
	List() ([]string, error)
	// Delete 删除 key 及其数据。
	Delete(key string) error
}

// FileStorage 基于文件系统的 LocalStorage 实现。
type FileStorage struct {
	baseDir string
}

// NewFileStorage 创建一个新的 FileStorage 实例。
// 若 baseDir 为空，使用默认路径 ".seele/storage/"。
func NewFileStorage(baseDir string) *FileStorage {
	if baseDir == "" {
		baseDir = ".seele/storage/"
	}
	return &FileStorage{baseDir: baseDir}
}

// Save 以 key 保存数据为 JSON 文件。
func (f *FileStorage) Save(key string, data interface{}) error {
	if err := os.MkdirAll(f.baseDir, 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f.filePath(key), b, 0644)
}

// Load 加载 key 对应的 JSON 文件到 target 中。
func (f *FileStorage) Load(key string, target interface{}) error {
	b, err := os.ReadFile(f.filePath(key))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

// List 返回存储目录下的所有 key（去掉 .json 后缀）。
func (f *FileStorage) List() ([]string, error) {
	entries, err := os.ReadDir(f.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var keys []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			keys = append(keys, e.Name()[:len(e.Name())-len(".json")])
		}
	}
	return keys, nil
}

// Delete 删除 key 对应的 JSON 文件。
func (f *FileStorage) Delete(key string) error {
	return os.Remove(f.filePath(key))
}

// filePath 返回 key 对应的完整文件路径。
func (f *FileStorage) filePath(key string) string {
	return filepath.Join(f.baseDir, key+".json")
}
