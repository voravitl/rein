package supervisor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type journal struct{ path string }
type journalEntry struct {
	Phase string          `json:"phase"`
	Raw   json.RawMessage `json:"raw,omitempty"`
}

// A check without its durable response is ambiguous and never retried blindly.
func (j journal) load() ([]byte, error) {
	b, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) > 0 && b[len(b)-1] != '\n' {
		return nil, errors.New("truncated supervisor journal")
	}
	phase := "consumed"
	var raw []byte
	for n, line := range bytes.Split(bytes.TrimSuffix(b, []byte{'\n'}), []byte{'\n'}) {
		if len(b) == 0 {
			break
		}
		var e journalEntry
		d := json.NewDecoder(bytes.NewReader(line))
		d.DisallowUnknownFields()
		if err := d.Decode(&e); err != nil {
			return nil, fmt.Errorf("journal line %d: %w", n+1, err)
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return nil, errors.New("journal line contains trailing JSON")
		}
		switch e.Phase {
		case "check":
			if phase != "consumed" {
				return nil, errors.New("invalid journal check transition")
			}
			raw = nil
		case "delivery":
			if phase != "check" || !json.Valid(e.Raw) {
				return nil, errors.New("invalid journal delivery transition")
			}
			raw = append([]byte(nil), e.Raw...)
		case "consumed":
			if phase != "delivery" {
				return nil, errors.New("invalid journal consume transition")
			}
			raw = nil
		default:
			return nil, errors.New("unknown journal transition")
		}
		phase = e.Phase
	}
	if phase == "check" {
		return nil, errors.New("unresolved Orca check intent; manual reconciliation required")
	}
	if raw != nil {
		f, err := os.OpenFile(j.path, os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
		if err := j.syncDir(); err != nil {
			return nil, err
		}
	}
	return raw, nil
}

func (j journal) append(e journalEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = j.syncDir()
	}
	return err
}

func (j journal) syncDir() error {
	d, err := os.Open(filepath.Dir(j.path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
