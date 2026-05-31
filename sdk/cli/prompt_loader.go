package cli

import (
	"log"
	"os"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// PromptLoader 监听 prompt 文件变化，始终返回最新内容。
// 用于 System Prompt 的热更新：修改文件无需重启进程。
type PromptLoader struct {
	path    string
	content string
	mu      sync.RWMutex
	watcher *fsnotify.Watcher
	done    chan struct{}
}

// NewPromptLoader 读取指定路径的 prompt 文件并启动文件监听。
// 返回的 loader 通过 Get() 始终返回最新内容。
func NewPromptLoader(path string) (*PromptLoader, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := watcher.Add(path); err != nil {
		watcher.Close()
		return nil, err
	}

	l := &PromptLoader{
		path:    path,
		content: string(content),
		watcher: watcher,
		done:    make(chan struct{}),
	}

	go l.watch()
	log.Printf("[PromptLoader] loaded %q (%d bytes), watching for changes", path, len(l.content))
	return l, nil
}

// Get 返回当前 prompt 内容（始终是最新的）。
func (l *PromptLoader) Get() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.content
}

// Reload 立即重新读取文件。返回最新内容。
func (l *PromptLoader) Reload() (string, error) {
	content, err := os.ReadFile(l.path)
	if err != nil {
		return "", err
	}
	l.mu.Lock()
	l.content = string(content)
	l.mu.Unlock()
	log.Printf("[PromptLoader] reloaded %q (%d bytes)", l.path, len(l.content))
	return l.content, nil
}

// Stop 关闭文件监听器。
func (l *PromptLoader) Stop() {
	close(l.done)
	l.watcher.Close()
}

func (l *PromptLoader) watch() {
	for {
		select {
		case <-l.done:
			return
		case event, ok := <-l.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				if _, err := l.Reload(); err != nil {
					log.Printf("[PromptLoader] reload failed: %v", err)
				}
			}
		case err, ok := <-l.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("[PromptLoader] watcher error: %v", err)
		}
	}
}
