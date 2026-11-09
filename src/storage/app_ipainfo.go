package storage

import (
	"SignTools/src/ipainfo"
	"encoding/json"

	"github.com/rs/zerolog/log"
)

// GetIpaInfo returns the name, version and size of an app's IPA, read once from its Info.plist and
// cached next to it. nil if the IPA can't be read (e.g. not an iOS app).
func GetIpaInfo(app App) *ipainfo.Info {
	if cached, err := app.GetString(AppIpaInfo); err == nil && cached != "" {
		var info ipainfo.Info
		if json.Unmarshal([]byte(cached), &info) == nil {
			if info.Size == 0 {
				return nil // cached failure
			}
			return &info
		}
	}
	return refreshIpaInfo(app)
}

func refreshIpaInfo(app App) *ipainfo.Info {
	path, err := app.GetAbsPath(AppUnsignedFile)
	if err != nil {
		return nil
	}
	info, err := ipainfo.Read(path)
	if err != nil {
		log.Debug().Err(err).Str("app_id", app.GetId()).Msg("read ipa info")
		// cache the failure as an empty object, so a broken IPA isn't re-read on every page load
		_ = app.SetString(AppIpaInfo, "{}")
		return nil
	}
	if data, err := json.Marshal(info); err == nil {
		_ = app.SetString(AppIpaInfo, string(data))
	}
	return info
}
