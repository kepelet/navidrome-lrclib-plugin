package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/scheduler"
	"github.com/navidrome/navidrome/plugins/pdk/go/scrobbler"
)

const (
	defaultBaseURL   = "https://lrclib.flooo.club"
	cacheKeyPrefix   = "lrclib.v1."
	kvstoreJobPrefix = "lrclib.job."
	cacheTTL         = int64(30 * 24 * 60 * 60)
	cacheValFound    = "found"
	cacheValNotFound = "notfound"
	userAgent        = "navidrome-lrclib/0.1.0 (https://github.com/kepelet/navidrome-lrclib)"
)

type jobInfo struct {
	TrackID     string  `json:"trackId"`
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album"`
	Duration    float32 `json:"duration"`
	SidecarPath string  `json:"sidecarPath"`
	Size        int64   `json:"size"`
	Suffix      string  `json:"suffix"`
}

type lrclibPlugin struct{}

func init() {
	scrobbler.Register(&lrclibPlugin{})
	scheduler.Register(&lrclibPlugin{})
}

func (p *lrclibPlugin) IsAuthorized(_ scrobbler.IsAuthorizedRequest) (bool, error) {
	return true, nil
}

func (p *lrclibPlugin) NowPlaying(req scrobbler.NowPlayingRequest) error {
	track := req.Track
	cacheKey := cacheKeyPrefix + track.ID

	if cached, exists, _ := host.CacheGetString(cacheKey); exists {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: cache hit for track %s (%s): %s", track.ID, track.Title, cached))

		return nil
	}

	if queued, _ := host.KVStoreHas(kvstoreJobPrefix + track.ID); queued {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: job already queued for track %s (%s)", track.ID, track.Title))

		return nil
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("lrclib: queuing lyrics fetch for %q by %q", track.Title, track.Artist))

	sidecarPath, err := resolveSidecarPath(req.Username, track.ID)

	if err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("lrclib: could not resolve path for track %s: %v", track.ID, err))

		return nil
	}

	job := &jobInfo{
		TrackID:     track.ID,
		Title:       track.Title,
		Artist:      track.Artist,
		Album:       track.Album,
		Duration:    track.Duration,
		SidecarPath: sidecarPath,
	}

	jobJSON, err := json.Marshal(job)

	if err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("lrclib: failed to marshal job for %q: %v", track.Title, err))

		return nil
	}

	kvKey := kvstoreJobPrefix + track.ID

	if err := host.KVStoreSet(kvKey, jobJSON); err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("lrclib: failed to store job for %q: %v", track.Title, err))

		return nil
	}

	if _, err := host.SchedulerScheduleOneTime(2, kvKey, ""); err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("lrclib: failed to schedule lyrics fetch for %q: %v", track.Title, err))

		_ = host.KVStoreDelete(kvKey)
	}

	return nil
}

func (p *lrclibPlugin) OnCallback(req scheduler.SchedulerCallbackRequest) error {
	kvKey := req.Payload

	jobJSON, exists, err := host.KVStoreGet(kvKey)

	if err != nil || !exists {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: job not found in KVStore for key %s", kvKey))

		return nil
	}

	_ = host.KVStoreDelete(kvKey)

	var job jobInfo

	if err := json.Unmarshal(jobJSON, &job); err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("lrclib: failed to unmarshal job %s: %v", kvKey, err))

		return nil
	}

	cacheKey := cacheKeyPrefix + job.TrackID

	if _, exists, _ := host.CacheGetString(cacheKey); exists {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: already processed %q, skipping", job.Title))

		return nil
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("lrclib: fetching lyrics for %q by %q", job.Title, job.Artist))

	trackInfo := scrobbler.TrackInfo{
		ID:       job.TrackID,
		Title:    job.Title,
		Artist:   job.Artist,
		Album:    job.Album,
		Duration: job.Duration,
	}

	lyrics, err := fetchLyrics(trackInfo)

	if err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("lrclib: fetch failed for %q: %v", job.Title, err))

		return nil
	}

	if lyrics == nil {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: no lyrics found for %q by %q", job.Title, job.Artist))

		_ = host.CacheSetString(cacheKey, cacheValNotFound, cacheTTL)

		return nil
	}

	if err := writeSidecar(job.SidecarPath, lyrics); err != nil {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("lrclib: failed to write sidecar for %q: %v", job.Title, err))

		return nil
	}

	_ = host.CacheSetString(cacheKey, cacheValFound, cacheTTL)

	pdk.Log(pdk.LogInfo, fmt.Sprintf("lrclib: wrote lyrics for %q >>> %s", job.Title, job.SidecarPath))

	return nil
}

func (p *lrclibPlugin) Scrobble(_ scrobbler.ScrobbleRequest) error {
	return nil
}

type lrclibResponse struct {
	ID           int     `json:"id"`
	TrackName    string  `json:"trackName"`
	ArtistName   string  `json:"artistName"`
	AlbumName    string  `json:"albumName"`
	Duration     float64 `json:"duration"`
	Instrumental bool    `json:"instrumental"`
	PlainLyrics  string  `json:"plainLyrics"`
	SyncedLyrics string  `json:"syncedLyrics"`
}

type lyricsResult struct {
	content   string
	extension string
}

func fetchLyrics(track scrobbler.TrackInfo) (*lyricsResult, error) {
	baseURL := defaultBaseURL

	if v, ok := pdk.GetConfig("baseurl"); ok && strings.TrimSpace(v) != "" {
		baseURL = strings.TrimRight(v, "/")
	}

	duration := int(math.Round(float64(track.Duration)))

	uri := fmt.Sprintf("%s/api/get?track_name=%s&artist_name=%s&album_name=%s&duration=%d",
		baseURL,
		urlEncode(track.Title),
		urlEncode(track.Artist),
		urlEncode(track.Album),
		duration,
	)

	pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: GET %s", uri))

	req := pdk.NewHTTPRequest(pdk.MethodGet, uri)

	req.SetHeader("User-Agent", userAgent)

	resp := req.Send()

	pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: response status %d", resp.Status()))

	if resp.Status() == 0 {
		return nil, fmt.Errorf("HTTP request failed (status 0 — host may have blocked the request or connection failed)")
	}

	if resp.Status() == 404 {
		return nil, nil
	}

	if resp.Status() != 200 {
		return nil, fmt.Errorf("lrclib returned HTTP %d", resp.Status())
	}

	var result lrclibResponse

	if err := json.Unmarshal(resp.Body(), &result); err != nil {
		return nil, fmt.Errorf("failed to parse lrclib response: %w", err)
	}

	if result.Instrumental {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: track %q is instrumental, skipping", track.Title))

		return nil, nil
	}

	if result.SyncedLyrics != "" {
		return &lyricsResult{content: result.SyncedLyrics, extension: ".lrc"}, nil
	}

	if result.PlainLyrics != "" {
		return &lyricsResult{content: result.PlainLyrics, extension: ".txt"}, nil
	}

	return nil, nil
}

type subsonicSongResponse struct {
	SubsonicResponse struct {
		Song struct {
			Path   string `json:"path"`
			Suffix string `json:"suffix"`
			Size   int64  `json:"size"`
		} `json:"song"`
	} `json:"subsonic-response"`
}

func resolveSidecarPath(username, trackID string) (string, error) {
	jsonStr, err := host.SubsonicAPICall("getSong?id=" + trackID + "&u=" + username + "&f=json")

	if err != nil {
		return "", fmt.Errorf("SubsonicAPICall failed: %w", err)
	}

	pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: getSong response: %s", jsonStr))

	var resp subsonicSongResponse

	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return "", fmt.Errorf("failed to parse getSong response: %w", err)
	}

	relPath := resp.SubsonicResponse.Song.Path
	suffix := resp.SubsonicResponse.Song.Suffix
	size := resp.SubsonicResponse.Song.Size

	if relPath == "" {
		return "", fmt.Errorf("getSong returned empty path for track %s", trackID)
	}

	libraries, err := host.LibraryGetAllLibraries()

	if err != nil {
		return "", fmt.Errorf("LibraryGetAllLibraries failed: %w", err)
	}

	for _, lib := range libraries {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: library id=%d name=%q path=%q mountPoint=%q",
			lib.ID, lib.Name, lib.Path, lib.MountPoint))

		root := lib.MountPoint

		if root == "" {
			root = lib.Path
		}

		if root == "" {
			continue
		}

		direct := filepath.Join(root, relPath)

		if _, err := os.Stat(direct); err == nil {
			ext := filepath.Ext(direct)
			sidecar := direct[:len(direct)-len(ext)]

			pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: audio file at direct path %s", direct))

			return sidecar, nil
		}

		pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: direct path %s not found, searching library for .%s file of size %d", direct, suffix, size))

		actualPath, searchErr := findAudioBySize(root, suffix, size)

		if searchErr != nil {
			pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: library search failed: %v", searchErr))
			continue
		}

		ext := filepath.Ext(actualPath)
		sidecar := actualPath[:len(actualPath)-len(ext)]

		pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: found audio at %s via size search, sidecar >>> %s{.lrc,.txt}", actualPath, sidecar))

		return sidecar, nil
	}

	return "", fmt.Errorf("could not locate audio file for track %s (path: %s)", trackID, relPath)
}

var errWalkStop = errors.New("stop walk")

func findAudioBySize(root, suffix string, size int64) (string, error) {
	if size <= 0 {
		return "", fmt.Errorf("invalid file size %d", size)
	}

	dotSuffix := "." + suffix

	var found string

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if info.IsDir() {
			return nil
		}

		if !strings.HasSuffix(path, dotSuffix) {
			return nil
		}

		if info.Size() == size {
			found = path

			return errWalkStop
		}

		return nil
	})

	if walkErr != nil && !errors.Is(walkErr, errWalkStop) {
		return "", walkErr
	}

	if found == "" {
		return "", fmt.Errorf("no .%s file of size %d found in %s", suffix, size, root)
	}

	return found, nil
}

func writeSidecar(basePath string, lyrics *lyricsResult) error {
	target := basePath + lyrics.extension

	if _, err := os.Stat(target); err == nil {
		pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: sidecar already exists at %s, skipping", target))

		return nil
	}

	pdk.Log(pdk.LogDebug, fmt.Sprintf("lrclib: writing sidecar to %s", target))

	if err := os.WriteFile(target, []byte(lyrics.content), 0644); err != nil {
		return fmt.Errorf("os.WriteFile(%s): %w", target, err)
	}

	return nil
}

func urlEncode(s string) string {
	var b strings.Builder

	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~':
			b.WriteRune(r)
		case r == ' ':
			b.WriteString("%20")
		default:
			b.WriteString(fmt.Sprintf("%%%02X", r))
		}
	}
	return b.String()
}

func main() {}
