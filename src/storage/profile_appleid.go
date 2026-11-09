package storage

import (
	"SignTools/src/util"
	"os"
	"regexp"
	"strings"

	"github.com/pkg/errors"
)

// An Apple ID signing profile: one team of an Apple ID logged in through signtools-portal.
// Certificates and provisioning profiles are created on demand while signing, like Impactor does.
const (
	ProfileAppleId    = FSName("apple_id.txt")
	ProfileTeamId     = FSName("team_id.txt")
	ProfileCertReset  = FSName("cert_reset.txt")
	appleIdProfileTag = "appleid-"
)

type appleIdProfile struct {
	id string
	FileSystemBase
}

func newAppleIdProfileFs(id string) *appleIdProfile {
	return &appleIdProfile{id: id, FileSystemBase: FileSystemBase{resolvePath: func(name FSName) string {
		return util.SafeJoinFilePaths(profilesPath, id, string(name))
	}}}
}

func isAppleIdProfileDir(id string) bool {
	_, err := os.Stat(util.SafeJoinFilePaths(profilesPath, id, string(ProfileAppleId)))
	return err == nil
}

func loadAppleIdProfile(id string) (*appleIdProfile, error) {
	p := newAppleIdProfileFs(id)
	for _, name := range []FSName{ProfileAppleId, ProfileTeamId, ProfileName} {
		if _, err := p.Stat(name); err != nil {
			return nil, errors.WithMessagef(err, "check required file %s", name)
		}
	}
	return p, nil
}

var nonIdChars = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// AppleIdProfileId is the profile id for an Apple ID team.
func AppleIdProfileId(email string, teamId string) string {
	return appleIdProfileTag + strings.ToLower(nonIdChars.ReplaceAllString(email, "-")) + "-" + nonIdChars.ReplaceAllString(teamId, "")
}

// CreateAppleIdProfile saves a signing profile for a team of an Apple ID and registers it.
func CreateAppleIdProfile(email string, teamId string, name string, allowCertReset bool) (Profile, error) {
	id := AppleIdProfileId(email, teamId)
	p := newAppleIdProfileFs(id)
	if err := os.MkdirAll(p.resolvePath(ProfileRoot), 0700); err != nil {
		return nil, err
	}
	reset := "0"
	if allowCertReset {
		reset = "1"
	}
	for fileName, value := range map[FSName]string{
		ProfileAppleId:   email,
		ProfileTeamId:    teamId,
		ProfileName:      name,
		ProfileCertReset: reset,
	} {
		if err := p.SetString(fileName, value); err != nil {
			return nil, err
		}
	}
	Profiles.add(p)
	return p, nil
}

// DeleteAppleIdProfiles removes all signing profiles of an Apple ID (on logout).
func DeleteAppleIdProfiles(email string) error {
	for _, profile := range Profiles.all() {
		p, ok := profile.(*appleIdProfile)
		if !ok {
			continue
		}
		if owner, _ := p.GetString(ProfileAppleId); owner != email {
			continue
		}
		if err := DeleteProfile(p.id); err != nil {
			return err
		}
	}
	return nil
}

// DeleteProfile removes a signing profile and its files (certificate, provisioning profile...).
// Profiles from environment variables can't be deleted here.
func DeleteProfile(id string) error {
	profile, ok := Profiles.GetById(id)
	if !ok {
		return errors.Errorf("no signing profile %s", id)
	}
	if _, isEnv := profile.(*envProfile); isEnv {
		return errors.New("this signing profile comes from environment variables, remove it there")
	}
	dir := util.SafeJoinFilePaths(profilesPath, id)
	if dir == util.SafeJoinFilePaths(profilesPath) {
		return errors.New("invalid profile id")
	}
	if err := os.RemoveAll(dir); err != nil {
		return errors.WithMessage(err, "remove profile files")
	}
	Profiles.remove(id)
	return nil
}

// ProfileKind describes a profile for the profile list: "Apple ID", "Certificate" or "Environment".
func ProfileKind(profile Profile) string {
	switch profile.(type) {
	case *appleIdProfile:
		return "Apple ID"
	case *envProfile:
		return "Environment"
	}
	if isAccount, _ := profile.IsAccount(); isAccount {
		return "Developer account"
	}
	return "Certificate"
}

func (p *appleIdProfile) GetId() string {
	return p.id
}

// IsAccount is false: that means the old fastlane developer-account mode.
func (p *appleIdProfile) IsAccount() (bool, error) {
	return false, nil
}

func (p *appleIdProfile) GetFiles() ([]fileGetter, error) {
	files := []fileGetter{
		{name: "apple_id.txt", f2: func() (string, error) { return p.GetString(ProfileAppleId) }},
		{name: "team_id.txt", f2: func() (string, error) { return p.GetString(ProfileTeamId) }},
	}
	if _, err := p.Stat(ProfileCertReset); err == nil {
		files = append(files, fileGetter{name: "cert_reset.txt", f2: func() (string, error) { return p.GetString(ProfileCertReset) }})
	}
	return files, nil
}

// IsAppleIdProfile tells the web UI that provisioning profiles are created per app.
func IsAppleIdProfile(profile Profile) bool {
	_, ok := profile.(*appleIdProfile)
	return ok
}
