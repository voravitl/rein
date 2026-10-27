package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/voravitl/rein/internal/run"
)

type PublishedOutput struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type ArtifactManifest struct {
	Version            int               `json:"version"`
	Run                string            `json:"run"`
	PlanHash           string            `json:"plan_hash"`
	Task               string            `json:"task"`
	Attempt            string            `json:"attempt"`
	Receipt            string            `json:"receipt"`
	ContractHash       string            `json:"contract_hash"`
	SourceRevision     string            `json:"source_revision"`
	SourceDigest       string            `json:"source_digest"`
	InputDigest        string            `json:"input_digest"`
	ExecutionDigest    string            `json:"execution_digest"`
	VerificationDigest string            `json:"verification_digest"`
	CheckpointDigest   string            `json:"checkpoint_digest"`
	Outputs            []PublishedOutput `json:"outputs"`
}
type ArtifactRef struct {
	Manifest ArtifactManifest `json:"manifest"`
	Digest   string           `json:"digest"`
}
type ResolvedInput struct {
	Producer string      `json:"producer"`
	Name     string      `json:"name"`
	As       string      `json:"as"`
	Artifact ArtifactRef `json:"artifact"`
}

// Rooted reads also reject links within the root. Capture happens only after
// termination proof, so an action cannot race component replacement.
func ReadArtifact(root, name string, max int64) ([]byte, error) {
	if !cleanArtifactPath(name) || max < 1 || max > 64<<20 {
		return nil, errors.New("invalid artifact path or bound")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	parts := strings.Split(name, "/")
	for i := range parts {
		st, err := r.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !st.IsDir()) {
			return nil, errors.New("artifact links and non-directory parents forbidden")
		}
	}
	before, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > max {
		return nil, errors.New("artifact must be regular and within bound")
	}
	// Nlink is not exposed portably by os.FileInfo. Unsupported platforms fail closed.
	sys := reflect.ValueOf(before.Sys())
	if sys.Kind() != reflect.Pointer || sys.IsNil() || sys.Elem().Kind() != reflect.Struct {
		return nil, errors.New("artifact link count unavailable")
	}
	links := sys.Elem().FieldByName("Nlink")
	if !links.IsValid() || !links.CanUint() || links.Uint() != 1 {
		return nil, errors.New("artifact hardlinks or unknown link count forbidden")
	}
	f, err := openArtifactFile(r, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("artifact replaced during open")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	after, err := r.Lstat(name)
	if err != nil || !os.SameFile(before, after) || int64(len(b)) > max || int64(len(b)) != before.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("artifact changed during capture")
	}
	return b, nil
}

// WriteImmutable creates a new durable file; it never replaces existing evidence.
func WriteImmutable(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return syncArtifactDir(filepath.Dir(path))
}
func syncArtifactDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	if e := f.Close(); err == nil {
		err = e
	}
	return err
}
func artifactDir(root string, m ArtifactManifest) string {
	return filepath.Join(root, m.Run, m.PlanHash, m.Task, m.Attempt)
}
func validateManifest(m ArtifactManifest) error {
	if m.Version != 1 || !validDigest(m.PlanHash) || !validDigest(m.ContractHash) || !validDigest(m.SourceDigest) || !validDigest(m.InputDigest) || !validDigest(m.ExecutionDigest) || !validDigest(m.VerificationDigest) || !validDigest(m.CheckpointDigest) || !fullRevision(m.SourceRevision) {
		return errors.New("incomplete artifact identity")
	}
	for _, s := range []string{m.Run, m.Task, m.Attempt, m.Receipt} {
		if !run.ValidName(s) {
			return errors.New("invalid manifest identity")
		}
	}
	return nil
}
func PublishArtifacts(root, source string, m ArtifactManifest, declared []ArtifactOutput) (ArtifactRef, error) {
	var empty ArtifactRef
	if err := validateManifest(m); err != nil {
		return empty, err
	}
	names := map[string]bool{}
	var total int64
	for _, o := range declared {
		if !run.ValidName(o.Name) || o.Name == "manifest.json" || names[o.Name] || !cleanArtifactPath(o.Path) || o.MaxBytes < 1 || o.MaxBytes > 64<<20 {
			return empty, errors.New("invalid artifact declaration")
		}
		names[o.Name] = true
		total += o.MaxBytes
	}
	if total > 64<<20 {
		return empty, errors.New("artifact declarations exceed total bound")
	}
	contents := make([][]byte, len(declared))
	m.Outputs = nil
	for i, o := range declared {
		b, err := ReadArtifact(source, o.Path, o.MaxBytes)
		if err != nil {
			return empty, fmt.Errorf("output %s: %w", o.Name, err)
		}
		contents[i] = b
		m.Outputs = append(m.Outputs, PublishedOutput{Name: o.Name, Path: o.Path, SHA256: digestBytes(b), Size: int64(len(b))})
	}
	dir := artifactDir(root, m)
	if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return empty, err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return empty, err
	}
	if err := syncArtifactDir(filepath.Dir(dir)); err != nil {
		return empty, err
	}
	for i, o := range m.Outputs {
		if err := WriteImmutable(filepath.Join(dir, o.Name), contents[i], 0400); err != nil {
			return empty, err
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return empty, err
	}
	if err := WriteImmutable(filepath.Join(dir, "manifest.json"), raw, 0400); err != nil {
		return empty, err
	}
	return ArtifactRef{m, digestBytes(raw)}, nil
}
func VerifyArtifacts(root string, ref ArtifactRef) error {
	if err := validateManifest(ref.Manifest); err != nil {
		return err
	}
	dir := artifactDir(root, ref.Manifest)
	raw, err := ReadArtifact(dir, "manifest.json", 1<<20)
	if err != nil {
		return err
	}
	expected, _ := json.Marshal(ref.Manifest)
	if !validDigest(ref.Digest) || digestBytes(raw) != ref.Digest || string(raw) != string(expected) {
		return errors.New("artifact manifest changed")
	}
	names := map[string]bool{}
	for _, o := range ref.Manifest.Outputs {
		if !cleanArtifactPath(o.Name) || !cleanArtifactPath(o.Path) || strings.Contains(o.Name, "/") || names[o.Name] || !validDigest(o.SHA256) || o.Size < 0 || o.Size > 64<<20 {
			return errors.New("invalid published output")
		}
		names[o.Name] = true
		b, err := ReadArtifact(dir, o.Name, 64<<20)
		if err != nil {
			return err
		}
		if int64(len(b)) != o.Size || digestBytes(b) != o.SHA256 {
			return errors.New("artifact content hash mismatch")
		}
	}
	return nil
}
func InputDigest(inputs []ResolvedInput) string { b, _ := json.Marshal(inputs); return digestBytes(b) }

// MaterializeInputs copies the very bytes that were hashed, rather than handing
// a consumer a mutable producer worktree path.
func MaterializeInputs(root, dest string, inputs []ResolvedInput) error {
	return materializeInputs(root, dest, inputs, ReadArtifact)
}

// The copy reader is per call; verification always uses the real rooted reader.
func materializeInputs(root, dest string, inputs []ResolvedInput, copyRead func(string, string, int64) ([]byte, error)) error {
	if err := os.Mkdir(dest, 0700); err != nil {
		return err
	}
	for _, in := range inputs {
		if !cleanArtifactPath(in.As) || strings.Contains(in.As, "/") {
			return errors.New("invalid consumer alias")
		}
		if in.Producer != in.Artifact.Manifest.Task {
			return errors.New("wrong producer binding")
		}
		if err := VerifyArtifacts(root, in.Artifact); err != nil {
			return err
		}
		found := false
		for _, o := range in.Artifact.Manifest.Outputs {
			if o.Name != in.Name {
				continue
			}
			found = true
			b, err := copyRead(artifactDir(root, in.Artifact.Manifest), o.Name, 64<<20)
			if err != nil {
				return err
			}
			if int64(len(b)) != o.Size || digestBytes(b) != o.SHA256 {
				return errors.New("input changed during materialization")
			}
			if err := WriteImmutable(filepath.Join(dest, in.As), b, 0444); err != nil {
				return err
			}
		}
		if !found {
			return errors.New("input output name missing")
		}
	}
	return os.Chmod(dest, 0555)
}
