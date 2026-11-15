package office

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Debounce is how long the watcher waits for writes to settle.
const Debounce = 300 * time.Millisecond

// Watch reloads OFFICE_DIR on changes until ctx ends.
func (r *Registry) Watch(ctx context.Context) error {
	if r.dir == "" {
		return nil
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(r.dir); err != nil {
		w.Close()
		return err
	}
	go r.loop(ctx, w)
	return nil
}

func (r *Registry) loop(ctx context.Context, w *fsnotify.Watcher) {
	defer w.Close()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if strings.HasSuffix(filepath.Base(ev.Name), ".json") {
				timer.Reset(Debounce)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			r.log.Warn("office watcher error", "err", err)
		case <-timer.C:
			r.ReloadFiles()
		}
	}
}
