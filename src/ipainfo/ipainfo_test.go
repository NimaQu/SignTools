package ipainfo

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func makeIpa(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.ipa")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, data := range entries {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReadBinaryPlist(t *testing.T) {
	path := makeIpa(t, map[string][]byte{
		"Payload/Zelda.app/Info.plist":                        mustRead(t, "Info.binary.plist"),
		"Payload/Zelda.app/PlugIns/Ext.appex/Info.plist":      []byte("not this one"),
		"Payload/Zelda.app/Frameworks/X.framework/Info.plist": []byte("nor this"),
		"Payload/Zelda.app/romfs.img":                         make([]byte, 1000),
	})
	info, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "塞尔达传说 王国之泪" || info.Version != "1.2.1" || info.Build != "121" ||
		info.BundleId != "org.nxnative.0100F2C0115B6000" || info.MinOS != "17.0" || info.Size <= 0 {
		t.Errorf("unexpected info %+v", info)
	}
	if got := info.VersionString(); got != "1.2.1 (121)" {
		t.Errorf("VersionString = %q", got)
	}
}

func TestReadXMLPlist(t *testing.T) {
	path := makeIpa(t, map[string][]byte{"Payload/Zelda.app/Info.plist": mustRead(t, "Info.xml.plist")})
	info, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	// no CFBundleDisplayName: falls back to CFBundleName; same version and build shown once
	if info.Name != "Zelda" || info.VersionString() != "1.2.1" {
		t.Errorf("unexpected info %+v", info)
	}
}

func TestNotAnIpa(t *testing.T) {
	if _, err := Read(makeIpa(t, map[string][]byte{"hello.txt": []byte("x")})); err == nil {
		t.Error("expected an error without Info.plist")
	}
	bad := filepath.Join(t.TempDir(), "bad.ipa")
	_ = os.WriteFile(bad, []byte("not a zip"), 0600)
	if _, err := Read(bad); err == nil {
		t.Error("expected an error for a non-zip file")
	}
}

func TestBrokenPlistsDontPanic(t *testing.T) {
	bin := mustRead(t, "Info.binary.plist")
	for i := 0; i < len(bin); i++ {
		_, _ = parsePlist(bin[:i])
		corrupt := append([]byte(nil), bin...)
		corrupt[i] ^= 0xff
		_, _ = parsePlist(corrupt)
	}
	_, _ = parsePlist(mustRead(t, "Info.xml.plist")[:200])
}

func TestFormatSize(t *testing.T) {
	for size, want := range map[int64]string{512: "512 B", 2048: "2.0 KiB", 17752757523: "16.5 GiB"} {
		if got := FormatSize(size); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", size, got, want)
		}
	}
}
