package controller

import (
	"io/fs"
	"log"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

func watchTemplates(wc *websocketController) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()
	done := make(chan bool)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write ||
					event.Op&fsnotify.Remove == fsnotify.Remove ||
					event.Op&fsnotify.Create == fsnotify.Create {
					m := &Operation{Op: Reload}
					wc.messageAll(m.Bytes())
					time.Sleep(1000 * time.Millisecond)
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}
		}
	}()

	for _, templatesPath := range wc.watchPaths {
		filepath.WalkDir(templatesPath, func(path string, d fs.DirEntry, err error) error {
			if d != nil && d.IsDir() {
				log.Println("watching =>", path)
				return watcher.Add(path)
			}
			return nil
		})
	}

	<-done
}
