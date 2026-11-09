package main

import (
	"SignTools/src/config"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveLocalTransferPath(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	mustWrite := func(p string) {
		if err := os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	inside := filepath.Join(base, "job", "signed.ipa")
	if err := os.MkdirAll(filepath.Dir(inside), 0700); err != nil {
		t.Fatal(err)
	}
	mustWrite(inside)
	secret := filepath.Join(outside, "secret")
	mustWrite(secret)
	link := filepath.Join(base, "link.ipa")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	old := config.Current
	defer func() { config.Current = old }()
	config.Current.File = &config.File{}

	config.Current.LocalTransferDir = ""
	if _, err := resolveLocalTransferPath(inside); err == nil {
		t.Error("accepted a path while local transfer is disabled")
	}

	config.Current.LocalTransferDir = base
	if p, err := resolveLocalTransferPath(inside); err != nil {
		t.Errorf("rejected a valid path: %v", err)
	} else if want, _ := filepath.EvalSymlinks(inside); p != want {
		t.Errorf("got %s, want %s", p, want)
	}
	for name, p := range map[string]string{
		"outside":           secret,
		"dot-dot traversal": filepath.Join(base, "job", "..", "..", filepath.Base(outside), "secret"),
		"symlink escape":    link,
		"directory":         filepath.Join(base, "job"),
		"base dir itself":   base,
		"relative":          "job/signed.ipa",
		"missing":           filepath.Join(base, "nope.ipa"),
	} {
		if _, err := resolveLocalTransferPath(p); err == nil {
			t.Errorf("%s: accepted %s", name, p)
		}
	}
}

func TestListImportFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"b.ipa", "a.IPA", ".hidden.ipa", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), make([]byte, 2048), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.ipa"), 0700); err != nil {
		t.Fatal(err)
	}
	old := config.Current
	defer func() { config.Current = old }()
	config.Current.File = &config.File{ImportDir: dir}
	files, err := listImportFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name != "a.IPA" || files[1].Name != "b.ipa" || files[0].Size != "2.0 KiB" {
		t.Errorf("unexpected import files: %+v", files)
	}
}
