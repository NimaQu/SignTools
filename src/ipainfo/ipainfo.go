// Package ipainfo reads an app's name, version and size from an IPA without extracting it: only the
// zip's central directory and the small Info.plist entry are read, even for a 20 GB archive.
package ipainfo

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

type Info struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Build    string `json:"build"`
	BundleId string `json:"bundle_id"`
	MinOS    string `json:"min_os"`
	Size     int64  `json:"size"`
}

var (
	iosInfoPlist = regexp.MustCompile(`^Payload/[^/]+\.app/Info\.plist$`)
	macInfoPlist = regexp.MustCompile(`^Payload/[^/]+\.app/Contents/Info\.plist$`)
)

const maxInfoPlistSize = 8 << 20

// Read returns the metadata of the IPA at path.
func Read(path string) (*Info, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var entry *zip.File
	for _, pattern := range []*regexp.Regexp{iosInfoPlist, macInfoPlist} {
		for _, f := range r.File {
			if pattern.MatchString(f.Name) {
				entry = f
				break
			}
		}
		if entry != nil {
			break
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("no Payload/*.app/Info.plist in %s", path)
	}
	if entry.UncompressedSize64 > maxInfoPlistSize {
		return nil, fmt.Errorf("Info.plist too large")
	}
	rc, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxInfoPlistSize))
	if err != nil {
		return nil, err
	}
	parsed, err := parsePlist(data)
	if err != nil {
		return nil, err
	}
	dict, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Info.plist is not a dictionary")
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := dict[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	return &Info{
		Name:     str("CFBundleDisplayName", "CFBundleName", "CFBundleExecutable"),
		Version:  str("CFBundleShortVersionString"),
		Build:    str("CFBundleVersion"),
		BundleId: str("CFBundleIdentifier"),
		MinOS:    str("MinimumOSVersion", "LSMinimumSystemVersion"),
		Size:     stat.Size(),
	}, nil
}

// VersionString is "1.2.0 (45)", or one of them if the other is missing or the same.
func (i *Info) VersionString() string {
	switch {
	case i.Version == "" || i.Version == i.Build:
		return i.Build
	case i.Build == "":
		return i.Version
	}
	return i.Version + " (" + i.Build + ")"
}

// FormatSize is a human readable size such as "16.5 GiB".
func FormatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}
