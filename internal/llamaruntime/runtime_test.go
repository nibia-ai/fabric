package llamaruntime

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestPinnedArtifactsCoverReleasePlatforms(t *testing.T) {
	tests := []struct{ os, arch, file, sum string }{
		{"darwin", "arm64", "llama-b10902-bin-macos-arm64.tar.gz", "9d6c0ac65ca25c3d2c5173ded6424b0b73ce147090fe56e78d70ae32bbeddfbe"},
		{"darwin", "amd64", "llama-b10902-bin-macos-x64.tar.gz", "6853a97493c66a0b610c0121a7e2b3092deb5ea36c756c24cac9e68cfb515cd4"},
		{"linux", "amd64", "llama-b10902-bin-ubuntu-x64.tar.gz", "471520d907a7dd861cc4bfe72a4c0ad938e9a82b656b31187f47e0ad5cceacb6"},
		{"linux", "arm64", "llama-b10902-bin-ubuntu-arm64.tar.gz", "eafe91e4ccb54257d2538509ea5e5fd8a175c525438f9d1b9c10983ea5bed011"},
		{"windows", "amd64", "llama-b10902-bin-win-cpu-x64.zip", "c435d6a751d44ee61f3ede886f24d7c330779c2789e48ca2a7d5bae87272308b"},
		{"windows", "arm64", "llama-b10902-bin-win-cpu-arm64.zip", "d4b88b288f5ec2bfb78e404b1a9de89224ad75294359c0b249002c049fddf423"},
	}
	for _, tt := range tests {
		a, ok := ArtifactFor(tt.os, tt.arch)
		if !ok {
			t.Fatalf("missing %s/%s", tt.os, tt.arch)
		}
		if a.File != tt.file || a.SHA256 != tt.sum {
			t.Fatalf("bad artifact for %s/%s: %+v", tt.os, tt.arch, a)
		}
	}
}

func TestSafeTargetRejectsTraversal(t *testing.T) {
	if _, err := safeTarget(t.TempDir(), "../../evil"); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

func TestExtractTarGzPreservesSafeSymlink(t *testing.T) {
	tmp := t.TempDir()
	archivePath := filepath.Join(tmp, "runtime.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	payload := []byte("library")
	if err := tw.WriteHeader(&tar.Header{Name: "bin/libmtmd.0.0.dylib", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "bin/libmtmd.0.dylib", Linkname: "libmtmd.0.0.dylib", Typeflag: tar.TypeSymlink}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tmp, "out")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(archivePath, dest); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dest, "bin", "libmtmd.0.dylib")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != "libmtmd.0.0.dylib" {
		t.Fatalf("unexpected symlink target %q", target)
	}
}
