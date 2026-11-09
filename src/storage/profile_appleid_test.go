package storage

import (
	"SignTools/src/config"
	"os"
	"testing"
)

func TestAppleIdProfileRoundTrip(t *testing.T) {
	oldPath := profilesPath
	profilesPath = t.TempDir()
	defer func() { profilesPath = oldPath }()
	Profiles = newProfileResolver()
	oldConfig := config.Current
	config.Current.EnvProfile = &config.EnvProfile{}
	defer func() { config.Current = oldConfig }()

	id := AppleIdProfileId("Jane.Doe+dev@Example.com", "FGHIJ67890")
	if id != "appleid-jane-doe-dev-example-com-FGHIJ67890" {
		t.Fatalf("unexpected id %s", id)
	}
	created, err := CreateAppleIdProfile("Jane.Doe+dev@Example.com", "FGHIJ67890", "Example Org", true)
	if err != nil {
		t.Fatal(err)
	}
	if !IsAppleIdProfile(created) {
		t.Fatal("not an Apple ID profile")
	}
	if got, ok := Profiles.GetById(id); !ok || got != created {
		t.Fatal("profile not registered")
	}

	// reload from disk like at startup
	Profiles = newProfileResolver()
	if err := Profiles.refresh(); err != nil {
		t.Fatal(err)
	}
	loaded, ok := Profiles.GetById(id)
	if !ok || !IsAppleIdProfile(loaded) {
		t.Fatal("profile not reloaded as Apple ID profile")
	}
	files, err := loaded.GetFiles()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"apple_id.txt": "Jane.Doe+dev@Example.com", "team_id.txt": "FGHIJ67890", "cert_reset.txt": "1"}
	if len(files) != len(want) {
		t.Fatalf("got %d files, want %d", len(files), len(want))
	}
	for _, f := range files {
		value, err := f.f2()
		if err != nil {
			t.Fatal(err)
		}
		if want[f.name] != value {
			t.Errorf("%s = %q, want %q", f.name, value, want[f.name])
		}
	}

	if err := DeleteAppleIdProfiles("Jane.Doe+dev@Example.com"); err != nil {
		t.Fatal(err)
	}
	if _, ok := Profiles.GetById(id); ok {
		t.Fatal("profile not removed on logout")
	}
}

func TestDeleteProfile(t *testing.T) {
	oldPath := profilesPath
	profilesPath = t.TempDir()
	defer func() { profilesPath = oldPath }()
	Profiles = newProfileResolver()

	p, err := CreateAppleIdProfile("a@b.c", "TEAM123456", "Team", false)
	if err != nil {
		t.Fatal(err)
	}
	dir := p.(*appleIdProfile).resolvePath(ProfileRoot)
	if err := DeleteProfile(p.GetId()); err != nil {
		t.Fatal(err)
	}
	if _, ok := Profiles.GetById(p.GetId()); ok {
		t.Error("still registered")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("profile dir not removed: %v", err)
	}
	if err := DeleteProfile(p.GetId()); err == nil {
		t.Error("deleting a missing profile should fail")
	}
	// the profiles dir itself must never be removed
	if _, err := os.Stat(profilesPath); err != nil {
		t.Errorf("profiles dir gone: %v", err)
	}
}
