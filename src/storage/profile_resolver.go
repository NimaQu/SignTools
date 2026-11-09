package storage

import (
	"SignTools/src/config"
	"SignTools/src/util"
	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"os"
	"sort"
	"sync"
)

func newProfileResolver() *profileResolver {
	return &profileResolver{
		idToProfileMap: map[string]Profile{},
	}
}

type profileResolver struct {
	mu             sync.RWMutex
	idToProfileMap map[string]Profile
}

func (r *profileResolver) add(profile Profile) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.idToProfileMap[profile.GetId()] = profile
}

func (r *profileResolver) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.idToProfileMap, id)
}

func (r *profileResolver) all() []Profile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var profiles []Profile
	for _, profile := range r.idToProfileMap {
		profiles = append(profiles, profile)
	}
	return profiles
}

func (r *profileResolver) refresh() error {
	idDirs, err := os.ReadDir(profilesPath)
	if err != nil {
		return errors.WithMessage(err, "read profiles dir")
	}
	idDirs = util.RemoveHiddenDirs(idDirs)
	envProfile, err := newEnvProfile(config.Current.EnvProfile)
	if err == nil {
		r.idToProfileMap[envProfile.GetId()] = envProfile
	} else if !os.IsNotExist(err) {
		return errors.WithMessage(err, "import profile from envvars")
	}
	for _, idDir := range idDirs {
		id := idDir.Name()
		if isAppleIdProfileDir(id) {
			profile, err := loadAppleIdProfile(id)
			if err != nil {
				log.Fatal().Err(err).Str("id", id).Msg("load Apple ID profile")
			}
			r.idToProfileMap[id] = profile
			continue
		}
		profile, err := loadProfile(id)
		if err != nil {
			log.Fatal().Err(err).Str("id", id).Msg("load profile from files")
		}
		r.idToProfileMap[id] = profile
	}
	return nil
}

func (r *profileResolver) GetAll() ([]Profile, error) {
	profiles := r.all()
	sort.Slice(profiles, func(i, j int) bool {
		name1, _ := profiles[i].GetString(ProfileName)
		name2, _ := profiles[j].GetString(ProfileName)
		return name1 < name2
	})
	return profiles, nil
}

func (r *profileResolver) GetById(id string) (Profile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	profile, ok := r.idToProfileMap[id]
	if !ok {
		return nil, false
	}
	return profile, true
}
