package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// idPattern: 파일 ID 는 서버가 만든 128비트 랜덤 hex 뿐이다.
// 경로를 만들기 전에 반드시 이 정규식으로 검증하므로 "../" 같은 값은 파일시스템에 닿지 못한다.
var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

var (
	ErrNotFound = errors.New("file not found")
	ErrQuota    = errors.New("storage quota exceeded")
)

// Meta 는 파일 1개의 메타데이터. <id>.json 으로 저장된다.
type Meta struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"` // 정제된 원본 이름 (표시용, 경로에 쓰이지 않음)
	Size       int64     `json:"size"`
	MIME       string    `json:"mime"` // 내용 기반으로 판별한 MIME
	SHA256     string    `json:"sha256"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	DeleteHash string    `json:"delete_hash"` // 삭제 키의 SHA-256 (원문은 저장하지 않음)
}

func (m *Meta) Expired(now time.Time) bool { return now.After(m.ExpiresAt) }

// CheckDeleteKey 는 상수 시간 비교로 타이밍 공격을 피한다.
func (m *Meta) CheckDeleteKey(key string) bool {
	return subtle.ConstantTimeCompare([]byte(hashKey(key)), []byte(m.DeleteHash)) == 1
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil { // crypto/rand: 예측 불가능한 난수
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Store 는 DATA_DIR 아래에 <id>.bin(내용) + <id>.json(메타)로 저장한다.
// 업로드 중인 파일은 DATA_DIR/tmp 에 쓰다가 검증이 끝나면 rename 으로 원자적으로 확정한다.
type Store struct {
	dir      string
	tmpDir   string
	maxTotal int64

	mu   sync.Mutex
	used int64
}

func NewStore(dir string, maxTotal int64) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: abs, tmpDir: filepath.Join(abs, "tmp"), maxTotal: maxTotal}
	if err := os.MkdirAll(s.tmpDir, 0o750); err != nil {
		return nil, err
	}
	s.Cleanup(time.Now())
	return s, nil
}

func (s *Store) dataPath(id string) string { return filepath.Join(s.dir, id+".bin") }
func (s *Store) metaPath(id string) string { return filepath.Join(s.dir, id+".json") }

// CreateTemp 는 0600 권한의 임시 파일을 만든다. 이름은 OS 가 정하므로 사용자 입력이 섞이지 않는다.
func (s *Store) CreateTemp() (*os.File, error) {
	return os.CreateTemp(s.tmpDir, "up-*")
}

// Commit 은 용량 한도를 확인하고 임시 파일을 최종 위치로 옮긴 뒤 메타를 기록한다.
func (s *Store) Commit(m *Meta, tmpPath string) error {
	if !idPattern.MatchString(m.ID) {
		return errors.New("invalid id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.used+m.Size > s.maxTotal {
		return ErrQuota
	}
	if err := os.Rename(tmpPath, s.dataPath(m.ID)); err != nil {
		return err
	}
	if err := s.writeMeta(m); err != nil {
		_ = os.Remove(s.dataPath(m.ID))
		return err
	}
	s.used += m.Size
	return nil
}

func (s *Store) writeMeta(m *Meta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.tmpDir, "meta-*")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmpName)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.metaPath(m.ID)); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func (s *Store) readMeta(id string) (*Meta, error) {
	b, err := os.ReadFile(s.metaPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Get 은 ID 형식 → 존재 → 만료 순으로 검사한다. 어느 단계에서 실패해도 호출자에게는 똑같이 ErrNotFound.
func (s *Store) Get(id string, now time.Time) (*Meta, error) {
	if !idPattern.MatchString(id) {
		return nil, ErrNotFound
	}
	m, err := s.readMeta(id)
	if err != nil {
		return nil, err
	}
	if m.Expired(now) {
		return nil, ErrNotFound
	}
	return m, nil
}

func (s *Store) Open(id string) (*os.File, error) {
	if !idPattern.MatchString(id) {
		return nil, ErrNotFound
	}
	f, err := os.Open(s.dataPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (s *Store) Delete(id string) error {
	if !idPattern.MatchString(id) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	m, err := s.readMeta(id)
	if err != nil {
		return err
	}
	s.removeFiles(id)
	s.used -= m.Size
	if s.used < 0 {
		s.used = 0
	}
	return nil
}

func (s *Store) removeFiles(id string) {
	_ = os.Remove(s.dataPath(id))
	_ = os.Remove(s.metaPath(id))
}

// Cleanup 은 만료된 파일과 오래된 임시 파일을 지우고 사용량을 다시 계산한다.
func (s *Store) Cleanup(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var used int64
	if entries, err := os.ReadDir(s.dir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".json") {
				continue
			}
			id := strings.TrimSuffix(name, ".json")
			if !idPattern.MatchString(id) {
				continue
			}
			m, err := s.readMeta(id)
			if err != nil {
				continue
			}
			if m.Expired(now) {
				s.removeFiles(id)
				continue
			}
			used += m.Size
		}
	}
	s.used = used

	if tmps, err := os.ReadDir(s.tmpDir); err == nil {
		for _, t := range tmps {
			if info, err := t.Info(); err == nil && now.Sub(info.ModTime()) > time.Hour {
				_ = os.Remove(filepath.Join(s.tmpDir, t.Name()))
			}
		}
	}
}

func (s *Store) CleanupLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.Cleanup(now)
		}
	}
}
