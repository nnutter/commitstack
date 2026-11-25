package commitstack

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	patchCacheMaxAge     = 30 * 24 * time.Hour
	patchCacheMaxBytes   = 16 << 20
	patchCacheMaxEntries = 10_000
	patchCacheScanLimit  = 50_000
)

var patchCacheEntryName = regexp.MustCompile(`^[a-f0-9]{64}\.patch$`)

func prunePatchCache(ctx context.Context, dir string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	folder, err := os.Open(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { err = errors.Join(err, folder.Close()) }()
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(dir), "maintenance.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil
		}
		return err
	}
	pruner := cachePruner{dir: dir, now: time.Now()}
	if err := pruner.scan(ctx, folder); err != nil {
		return err
	}
	return pruner.evict(ctx)
}

type cacheEntry struct {
	name     string
	size     int64
	modified time.Time
}

type cachePruner struct {
	dir  string
	now  time.Time
	kept []cacheEntry
	size int64
}

func (p *cachePruner) collect(item os.DirEntry) error {
	isEntry := patchCacheEntryName.MatchString(item.Name())
	if !isEntry && !strings.HasPrefix(item.Name(), ".patch-") {
		return nil
	}
	if !item.Type().IsRegular() {
		return nil
	}
	info, err := item.Info()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if expiredCacheEntry(isEntry, p.now.Sub(info.ModTime())) {
		return removeIfPresent(filepath.Join(p.dir, item.Name()))
	}
	if isEntry {
		p.kept = append(p.kept, cacheEntry{name: item.Name(), size: info.Size(), modified: info.ModTime()})
		p.size += info.Size()
	}
	return nil
}

func (p *cachePruner) evict(ctx context.Context) error {
	slices.SortFunc(p.kept, func(a, b cacheEntry) int { return a.modified.Compare(b.modified) })
	for i, item := range p.kept {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.withinLimits(len(p.kept) - i) {
			break
		}
		if err := removeIfPresent(filepath.Join(p.dir, item.name)); err != nil {
			return err
		}
		p.size -= item.size
	}
	return nil
}

func (p *cachePruner) scan(ctx context.Context, folder *os.File) error {
	for scanned := 0; scanned < patchCacheScanLimit; {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := folder.ReadDir(min(128, patchCacheScanLimit-scanned))
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, item := range entries {
			scanned++
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := p.collect(item); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return nil
}

func (p *cachePruner) withinLimits(count int) bool {
	if count > patchCacheMaxEntries {
		return false
	}
	return p.size <= patchCacheMaxBytes
}

func expiredCacheEntry(isEntry bool, age time.Duration) bool {
	if age > patchCacheMaxAge {
		return true
	}
	if isEntry {
		return false
	}
	return age > 24*time.Hour
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
