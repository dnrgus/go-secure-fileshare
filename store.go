package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// idPattern: 파일 ID 는 서버가 만든 128비트 랜덤 hex 뿐이다.
// 경로를 만들기 전에 반드시 이 정규식으로 검증하므로 "../" 같은 값은 파일시스템에 닿지 못한다.
var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

var ErrNotFound = errors.New("file not found")

// Meta 는 파일 1개의 메타데이터. <id>.json 으로 저장된다.
type Meta struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"` // 원본 이름 (표시용, 경로에 쓰이지 않음)
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil { // crypto/rand: 예측 불가능한 난수
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Store 는 DATA_DIR 아래에 <id>.bin(내용) + <id>.json(메타)로 저장한다.
// 업로드 중인 파일은 DATA_DIR/tmp 에 쓰다가 rename 으로 원자적으로 확정한다.
type Store struct {
	dir    string
	tmpDir string
}

func NewStore(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: abs, tmpDir: filepath.Join(abs, "tmp")}
	if err := os.MkdirAll(s.tmpDir, 0o750); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) dataPath(id string) string { return filepath.Join(s.dir, id+".bin") }
func (s *Store) metaPath(id string) string { return filepath.Join(s.dir, id+".json") }

// CreateTemp 는 0600 권한의 임시 파일을 만든다. 이름은 OS 가 정하므로 사용자 입력이 섞이지 않는다.
func (s *Store) CreateTemp() (*os.File, error) {
	return os.CreateTemp(s.tmpDir, "up-*")
}

// Commit 은 임시 파일을 최종 위치로 옮긴 뒤 메타를 기록한다.
func (s *Store) Commit(m *Meta, tmpPath string) error {
	if !idPattern.MatchString(m.ID) {
		return errors.New("invalid id")
	}
	if err := os.Rename(tmpPath, s.dataPath(m.ID)); err != nil {
		return err
	}
	if err := s.writeMeta(m); err != nil {
		_ = os.Remove(s.dataPath(m.ID))
		return err
	}
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

// Get 은 ID 형식 → 존재 순으로 검사한다. 어느 단계에서 실패해도 호출자에게는 똑같이 ErrNotFound.
func (s *Store) Get(id string) (*Meta, error) {
	if !idPattern.MatchString(id) {
		return nil, ErrNotFound
	}
	return s.readMeta(id)
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
