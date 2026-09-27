package llamaruntime

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"
)

const (
	Tag        = "b10902"
	Commit     = "df03399"
	CommitFull = "df03399b885831b2a1603b3abb0d8c156808e363"
	BaseURL    = "https://github.com/ggml-org/llama.cpp/releases/download/" + Tag + "/"
)

type Artifact struct {
	OS     string
	Arch   string
	File   string
	SHA256 string
}

var artifacts = []Artifact{
	{"darwin", "arm64", "llama-b10902-bin-macos-arm64.tar.gz", "9d6c0ac65ca25c3d2c5173ded6424b0b73ce147090fe56e78d70ae32bbeddfbe"},
	{"darwin", "amd64", "llama-b10902-bin-macos-x64.tar.gz", "6853a97493c66a0b610c0121a7e2b3092deb5ea36c756c24cac9e68cfb515cd4"},
	{"linux", "amd64", "llama-b10902-bin-ubuntu-x64.tar.gz", "471520d907a7dd861cc4bfe72a4c0ad938e9a82b656b31187f47e0ad5cceacb6"},
	{"linux", "arm64", "llama-b10902-bin-ubuntu-arm64.tar.gz", "eafe91e4ccb54257d2538509ea5e5fd8a175c525438f9d1b9c10983ea5bed011"},
	{"windows", "amd64", "llama-b10902-bin-win-cpu-x64.zip", "c435d6a751d44ee61f3ede886f24d7c330779c2789e48ca2a7d5bae87272308b"},
	{"windows", "arm64", "llama-b10902-bin-win-cpu-arm64.zip", "d4b88b288f5ec2bfb78e404b1a9de89224ad75294359c0b249002c049fddf423"},
}

type Metadata struct {
	Tag         string    `json:"tag"`
	Commit      string    `json:"commit"`
	Asset       string    `json:"asset"`
	SHA256      string    `json:"sha256"`
	BinDir      string    `json:"bin_dir"`
	Version     string    `json:"version"`
	InstalledAt time.Time `json:"installed_at"`
}

type Status struct {
	Installed bool
	Root      string
	BinDir    string
	Version   string
	Artifact  Artifact
}

func ArtifactFor(goos, goarch string) (Artifact, bool) {
	for _, a := range artifacts {
		if a.OS == goos && a.Arch == goarch {
			return a, true
		}
	}
	return Artifact{}, false
}

func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(".nibia", "runtime", "llama.cpp", Tag, goruntime.GOOS+"-"+goruntime.GOARCH)
	}
	return filepath.Join(home, ".nibia", "runtime", "llama.cpp", Tag, goruntime.GOOS+"-"+goruntime.GOARCH)
}

func metadataPath(root string) string { return filepath.Join(root, "runtime.json") }

func binaryFilename(name string) string {
	if goruntime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name + ".exe"
	}
	return name
}

func loadMetadata(root string) (Metadata, error) {
	b, err := os.ReadFile(metadataPath(root))
	if err != nil {
		return Metadata{}, err
	}
	var m Metadata
	if err := json.Unmarshal(b, &m); err != nil {
		return Metadata{}, err
	}
	if m.Tag != Tag || !strings.HasPrefix(strings.ToLower(m.Commit), Commit) {
		return Metadata{}, fmt.Errorf("managed runtime identity mismatch")
	}
	if m.BinDir == "" {
		return Metadata{}, errors.New("managed runtime metadata has no bin_dir")
	}
	return m, nil
}

func StatusCurrent() Status {
	a, _ := ArtifactFor(goruntime.GOOS, goruntime.GOARCH)
	root := DefaultRoot()
	m, err := loadMetadata(root)
	if err != nil {
		return Status{Root: root, Artifact: a}
	}
	binDir := filepath.Join(root, filepath.FromSlash(m.BinDir))
	for _, name := range []string{"llama-cli", "llama-server", "ggml-rpc-server"} {
		if st, err := os.Stat(filepath.Join(binDir, binaryFilename(name))); err != nil || st.IsDir() {
			return Status{Root: root, Artifact: a}
		}
	}
	return Status{Installed: true, Root: root, BinDir: binDir, Version: m.Version, Artifact: a}
}

// ResolveBinary prefers NIBIA's pinned managed runtime and falls back to PATH
// for development/backward compatibility.
func ResolveBinary(name string) (string, string, error) {
	st := StatusCurrent()
	if st.Installed {
		p := filepath.Join(st.BinDir, binaryFilename(name))
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			activateManagedLibraryPath(st.BinDir)
			return p, "managed", nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, "external", nil
	}
	return "", "", fmt.Errorf("%s not found", name)
}

// activateManagedLibraryPath prepares the current NIBIA process so all child
// llama.cpp processes can resolve the shared libraries shipped beside the
// official prebuilt binaries. The change is process-local; it does not modify
// the user's shell or system configuration.
func activateManagedLibraryPath(binDir string) {
	var key string
	switch goruntime.GOOS {
	case "darwin":
		key = "DYLD_LIBRARY_PATH"
	case "linux":
		key = "LD_LIBRARY_PATH"
	default:
		return
	}
	binDir = strings.TrimSpace(binDir)
	if binDir == "" {
		return
	}
	old := os.Getenv(key)
	for _, entry := range filepath.SplitList(old) {
		if entry == binDir {
			return
		}
	}
	if old == "" {
		_ = os.Setenv(key, binDir)
		return
	}
	_ = os.Setenv(key, binDir+string(os.PathListSeparator)+old)
}

func commandWithManagedLibraries(ctx context.Context, path, binDir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, path, args...)
	if goruntime.GOOS == "darwin" || goruntime.GOOS == "linux" {
		key := "DYLD_LIBRARY_PATH"
		if goruntime.GOOS == "linux" {
			key = "LD_LIBRARY_PATH"
		}
		value := binDir
		if old := os.Getenv(key); old != "" {
			value += string(os.PathListSeparator) + old
		}
		cmd.Env = append(os.Environ(), key+"="+value)
	}
	return cmd
}

func ResolveOrEnsure(ctx context.Context, name string, progress io.Writer) (string, string, error) {
	if p, source, err := ResolveBinary(name); err == nil {
		return p, source, nil
	}
	if _, err := Ensure(ctx, progress); err != nil {
		return "", "", err
	}
	return ResolveBinary(name)
}

func Ensure(ctx context.Context, progress io.Writer) (Status, error) {
	if progress == nil {
		progress = io.Discard
	}
	if st := StatusCurrent(); st.Installed {
		return st, nil
	}
	a, ok := ArtifactFor(goruntime.GOOS, goruntime.GOARCH)
	if !ok {
		return Status{}, fmt.Errorf("no pinned llama.cpp runtime for %s/%s", goruntime.GOOS, goruntime.GOARCH)
	}

	root := DefaultRoot()
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Status{}, err
	}
	work, err := os.MkdirTemp(parent, ".runtime-"+Tag+"-")
	if err != nil {
		return Status{}, err
	}
	defer os.RemoveAll(work)

	archive := filepath.Join(work, a.File)
	fmt.Fprintf(progress, "llama.cpp runtime: downloading %s for %s/%s...\n", Tag, goruntime.GOOS, goruntime.GOARCH)
	if err := download(ctx, BaseURL+a.File, archive); err != nil {
		return Status{}, fmt.Errorf("download pinned llama.cpp runtime: %w", err)
	}
	got, err := fileSHA256(archive)
	if err != nil {
		return Status{}, err
	}
	if !strings.EqualFold(got, a.SHA256) {
		return Status{}, fmt.Errorf("llama.cpp runtime SHA-256 mismatch: got %s want %s", got, a.SHA256)
	}
	fmt.Fprintln(progress, "llama.cpp runtime: SHA-256 verified")

	extractDir := filepath.Join(work, "extract")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return Status{}, err
	}
	if strings.HasSuffix(a.File, ".zip") {
		if err := extractZip(archive, extractDir); err != nil {
			return Status{}, err
		}
	} else {
		if err := extractTarGz(archive, extractDir); err != nil {
			return Status{}, err
		}
	}
	binDir, err := findBinDir(extractDir)
	if err != nil {
		return Status{}, err
	}
	if goruntime.GOOS != "windows" {
		for _, name := range []string{"llama-cli", "llama-server", "ggml-rpc-server"} {
			_ = os.Chmod(filepath.Join(binDir, name), 0o755)
		}
	}
	rel, err := filepath.Rel(extractDir, binDir)
	if err != nil {
		return Status{}, err
	}

	// Install the verified archive before executing its binaries. On Windows,
	// launching an executable from the staging directory can leave short-lived
	// loader/Defender handles behind, making a directory rename fail with
	// ACCESS_DENIED even though the process already exited. Moving first avoids
	// that class of failure.
	if err := installExtractedRuntime(extractDir, root); err != nil {
		return Status{}, fmt.Errorf("install managed llama.cpp runtime: %w", err)
	}
	installedBinDir := filepath.Join(root, rel)
	version, err := verifyManagedRuntime(ctx, installedBinDir)
	if err != nil {
		_ = os.RemoveAll(root)
		return Status{}, err
	}

	meta := Metadata{Tag: Tag, Commit: CommitFull, Asset: a.File, SHA256: a.SHA256, BinDir: filepath.ToSlash(rel), Version: version, InstalledAt: time.Now().UTC()}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "runtime.json"), append(mb, '\n'), 0o644); err != nil {
		_ = os.RemoveAll(root)
		return Status{}, err
	}
	st := StatusCurrent()
	if !st.Installed {
		return Status{}, errors.New("managed llama.cpp runtime installation did not validate")
	}
	fmt.Fprintf(progress, "llama.cpp runtime: ready %s (%s)\n", Tag, Commit)
	return st, nil
}

func installExtractedRuntime(extractDir, root string) error {
	if err := removeAllWithRetry(root, 8, 150*time.Millisecond); err != nil {
		return err
	}
	// Fast/atomic path first. This normally succeeds on Unix and on Windows
	// when no scanner has a transient handle on the staging tree.
	if err := renameWithRetry(extractDir, root, 8, 150*time.Millisecond); err == nil {
		return nil
	} else if goruntime.GOOS != "windows" {
		return err
	}

	// Windows fallback: copy the staging tree into the final location. Reads are
	// typically permitted even while an AV/indexer temporarily prevents rename
	// or delete sharing. The staging directory is removed by Ensure's defer.
	if err := copyTree(extractDir, root); err != nil {
		_ = os.RemoveAll(root)
		return err
	}
	return nil
}

func renameWithRetry(src, dst string, attempts int, delay time.Duration) error {
	var last error
	for i := 0; i < attempts; i++ {
		if err := os.Rename(src, dst); err == nil {
			return nil
		} else {
			last = err
		}
		if i+1 < attempts {
			time.Sleep(delay)
		}
	}
	return last
}

func removeAllWithRetry(path string, attempts int, delay time.Duration) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var last error
	for i := 0; i < attempts; i++ {
		if err := os.RemoveAll(path); err == nil {
			if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
				return nil
			}
			last = fmt.Errorf("path still exists after removal: %s", path)
		} else {
			last = err
		}
		if i+1 < attempts {
			time.Sleep(delay)
		}
	}
	return last
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeOutErr := out.Close()
		closeInErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
		return closeInErr
	})
}

func download(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 20 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extractZip(path, dest string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		target, err := safeTarget(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, e1 := io.Copy(out, rc)
		e2 := out.Close()
		e3 := rc.Close()
		if e1 != nil {
			return e1
		}
		if e2 != nil {
			return e2
		}
		if e3 != nil {
			return e3
		}
	}
	return nil
}

func extractTarGz(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target, err := safeTarget(dest, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode))
			if err != nil {
				return err
			}
			_, e1 := io.Copy(out, tr)
			e2 := out.Close()
			if e1 != nil {
				return e1
			}
			if e2 != nil {
				return e2
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("unsafe archive symlink %q -> %q", h.Name, h.Linkname)
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(target), filepath.FromSlash(h.Linkname)))
			rel, err := filepath.Rel(dest, resolved)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("unsafe archive symlink %q -> %q", h.Name, h.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(filepath.FromSlash(h.Linkname), target); err != nil {
				return err
			}
		case tar.TypeLink:
			linkTarget, err := safeTarget(dest, h.Linkname)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func safeTarget(root, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return target, nil
}

func findBinDir(root string) (string, error) {
	want := map[string]bool{}
	for _, n := range []string{"llama-cli", "llama-server", "ggml-rpc-server"} {
		want[strings.ToLower(binaryFilename(n))] = true
	}
	counts := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if want[strings.ToLower(d.Name())] {
			counts[filepath.Dir(path)]++
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	for dir, n := range counts {
		if n == len(want) {
			return dir, nil
		}
	}
	return "", errors.New("pinned llama.cpp archive does not contain llama-cli, llama-server, and ggml-rpc-server in one directory")
}

func verifyManagedRuntime(ctx context.Context, binDir string) (string, error) {
	// The pinned archive SHA-256 establishes artifact identity. This executable
	// probe is a separate health check so loader/startup failures are reported
	// as such instead of being mislabeled as commit mismatches. macOS may take
	// longer on the first launch of freshly downloaded binaries, so allow a
	// generous per-binary startup window.
	var diagnostics []string
	for _, name := range []string{"llama-server", "llama-cli"} {
		path := filepath.Join(binDir, binaryFilename(name))
		version, output, err := binaryVersion(ctx, path, binDir)
		if version != "" && strings.Contains(strings.ToLower(output), Commit) {
			return version, nil
		}
		if version != "" && err == nil {
			return "", fmt.Errorf("llama.cpp runtime identity mismatch after verified archive: expected commit %s, got %q", Commit, version)
		}

		detail := strings.TrimSpace(output)
		if detail == "" {
			detail = "no output"
		}
		if len(detail) > 400 {
			detail = detail[:400] + "..."
		}
		if err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("%s --version: %v (%s)", name, err, detail))
		} else {
			diagnostics = append(diagnostics, fmt.Sprintf("%s --version: %s", name, detail))
		}
	}
	return "", fmt.Errorf("llama.cpp runtime executable check failed after SHA-256 verification: %s", strings.Join(diagnostics, "; "))
}

func binaryVersion(ctx context.Context, path, binDir string) (string, string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := commandWithManagedLibraries(cctx, path, binDir, "--version").CombinedOutput()
	text := strings.TrimSpace(string(out))
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line, text, err
		}
	}
	return "", text, err
}
