package ctrlsubsonic

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/xyproto/randomstring"
	"go.senan.xyz/gonic/db"
	"go.senan.xyz/gonic/handlerutil"
	"go.senan.xyz/gonic/infocache/albuminfocache"
	"go.senan.xyz/gonic/infocache/artistinfocache"
	"go.senan.xyz/gonic/jukebox"
	"go.senan.xyz/gonic/lastfm"
	"go.senan.xyz/gonic/playlist"
	"go.senan.xyz/gonic/podcast"
	"go.senan.xyz/gonic/scanner"
	"go.senan.xyz/gonic/scrobble"
	"go.senan.xyz/gonic/server/ctrlsubsonic/params"
	"go.senan.xyz/gonic/server/ctrlsubsonic/spec"
	"go.senan.xyz/gonic/transcode"
)

type CtxKey int

const (
	CtxUser CtxKey = iota
	CtxSession
	CtxParams
)

type MusicPath struct {
	Alias, Path string
}

func MusicPaths(paths []MusicPath) []string {
	var r []string
	for _, p := range paths {
		r = append(r, p.Path)
	}
	return r
}

type ProxyPathResolver func(in string) string

type Controller struct {
	*http.ServeMux

	dbc              *db.DB
	scanner          *scanner.Scanner
	musicPaths       []MusicPath
	podcastsPath     string
	cacheAudioPath   string
	cacheCoverPath   string
	jukebox          *jukebox.Jukebox
	playlistStore    *playlist.Store
	scrobblers       []scrobble.Scrobbler
	podcasts         *podcast.Podcasts
	transcoder       transcode.Transcoder
	lastFMClient     *lastfm.Client
	artistInfoCache  *artistinfocache.ArtistInfoCache
	albumInfoCache   *albuminfocache.AlbumInfoCache
	resolveProxyPath ProxyPathResolver
}

func New(dbc *db.DB, scannr *scanner.Scanner, musicPaths []MusicPath, podcastsPath string, cacheAudioPath string, cacheCoverPath string, jukebox *jukebox.Jukebox, playlistStore *playlist.Store, scrobblers []scrobble.Scrobbler, podcasts *podcast.Podcasts, transcoder transcode.Transcoder, lastFMClient *lastfm.Client, artistInfoCache *artistinfocache.ArtistInfoCache, albumInfoCache *albuminfocache.AlbumInfoCache, resolveProxyPath ProxyPathResolver) (*Controller, error) {
	c := Controller{
		ServeMux: http.NewServeMux(),

		dbc:              dbc,
		scanner:          scannr,
		musicPaths:       musicPaths,
		podcastsPath:     podcastsPath,
		cacheAudioPath:   cacheAudioPath,
		cacheCoverPath:   cacheCoverPath,
		jukebox:          jukebox,
		playlistStore:    playlistStore,
		scrobblers:       scrobblers,
		podcasts:         podcasts,
		transcoder:       transcoder,
		lastFMClient:     lastFMClient,
		artistInfoCache:  artistInfoCache,
		albumInfoCache:   albumInfoCache,
		resolveProxyPath: resolveProxyPath,
	}

	chain := handlerutil.Chain(
		withParams,
		withRequiredParams,
		withUser(dbc),
	)
	chainRaw := handlerutil.Chain(
		chain,
		slow,
	)

	c.Handle("/getLicense", chain(resp(c.ServeGetLicence)))
	c.Handle("/ping", chain(resp(c.ServePing)))
	c.Handle("/getOpenSubsonicExtensions", chain(resp(c.ServeGetOpenSubsonicExtensions)))

	c.Handle("/getMusicFolders", chain(resp(c.ServeGetMusicFolders)))
	c.Handle("/getScanStatus", chain(resp(c.ServeGetScanStatus)))
	c.Handle("/scrobble", chain(resp(c.ServeScrobble)))
	c.Handle("/startScan", chain(resp(c.ServeStartScan)))
	c.Handle("/getUser", chain(resp(c.ServeGetUser)))
	c.Handle("/getPlaylists", chain(resp(c.ServeGetPlaylists)))
	c.Handle("/getPlaylist", chain(resp(c.ServeGetPlaylist)))
	c.Handle("/createPlaylist", chain(resp(c.ServeCreateOrUpdatePlaylist)))
	c.Handle("/updatePlaylist", chain(resp(c.ServeUpdatePlaylist)))
	c.Handle("/deletePlaylist", chain(resp(c.ServeDeletePlaylist)))
	c.Handle("/savePlayQueue", chain(resp(c.ServeSavePlayQueue)))
	c.Handle("/getPlayQueue", chain(resp(c.ServeGetPlayQueue)))
	c.Handle("/getSong", chain(resp(c.ServeGetSong)))
	c.Handle("/getRandomSongs", chain(resp(c.ServeGetRandomSongs)))
	c.Handle("/getSongsByGenre", chain(resp(c.ServeGetSongsByGenre)))
	c.Handle("/jukeboxControl", chain(resp(c.ServeJukebox)))
	c.Handle("/getBookmarks", chain(resp(c.ServeGetBookmarks)))
	c.Handle("/createBookmark", chain(resp(c.ServeCreateBookmark)))
	c.Handle("/deleteBookmark", chain(resp(c.ServeDeleteBookmark)))
	c.Handle("/getTopSongs", chain(resp(c.ServeGetTopSongs)))
	c.Handle("/getSimilarSongs", chain(resp(c.ServeGetSimilarSongs)))
	c.Handle("/getSimilarSongs2", chain(resp(c.ServeGetSimilarSongsTwo)))
	c.Handle("/getLyrics", chain(resp(c.ServeGetLyrics)))

	// raw
	c.Handle("/getCoverArt", chainRaw(respRaw(c.ServeGetCoverArt)))
	c.Handle("/stream", chainRaw(respRaw(c.ServeStream)))
	c.Handle("/download", chainRaw(respRaw(c.ServeStream)))
	c.Handle("/getAvatar", chainRaw(respRaw(c.ServeGetAvatar)))

	// browse by tag
	c.Handle("/getAlbum", chain(resp(c.ServeGetAlbum)))
	c.Handle("/getAlbumList2", chain(resp(c.ServeGetAlbumListTwo)))
	c.Handle("/getArtist", chain(resp(c.ServeGetArtist)))
	c.Handle("/getArtists", chain(resp(c.ServeGetArtists)))
	c.Handle("/search3", chain(resp(c.ServeSearchThree)))
	c.Handle("/getStarred2", chain(resp(c.ServeGetStarredTwo)))
	c.Handle("/getArtistInfo2", chain(resp(c.ServeGetArtistInfoTwo)))
	c.Handle("/getAlbumInfo2", chain(resp(c.ServeGetAlbumInfoTwo)))

	// browse by folder
	c.Handle("/getIndexes", chain(resp(c.ServeGetIndexes)))
	c.Handle("/getMusicDirectory", chain(resp(c.ServeGetMusicDirectory)))
	c.Handle("/getAlbumList", chain(resp(c.ServeGetAlbumList)))
	c.Handle("/search2", chain(resp(c.ServeSearchTwo)))
	c.Handle("/getGenres", chain(resp(c.ServeGetGenres)))
	c.Handle("/getArtistInfo", chain(resp(c.ServeGetArtistInfo)))
	c.Handle("/getStarred", chain(resp(c.ServeGetStarred)))

	// star / rating
	c.Handle("/star", chain(resp(c.ServeStar)))
	c.Handle("/unstar", chain(resp(c.ServeUnstar)))
	c.Handle("/setRating", chain(resp(c.ServeSetRating)))

	// podcasts
	c.Handle("/getPodcasts", chain(resp(c.ServeGetPodcasts)))
	c.Handle("/getNewestPodcasts", chain(resp(c.ServeGetNewestPodcasts)))
	c.Handle("/downloadPodcastEpisode", chain(resp(c.ServeDownloadPodcastEpisode)))
	c.Handle("/createPodcastChannel", chain(resp(c.ServeCreatePodcastChannel)))
	c.Handle("/refreshPodcasts", chain(resp(c.ServeRefreshPodcasts)))
	c.Handle("/deletePodcastChannel", chain(resp(c.ServeDeletePodcastChannel)))
	c.Handle("/deletePodcastEpisode", chain(resp(c.ServeDeletePodcastEpisode)))

	// internet radio
	c.Handle("/getInternetRadioStations", chain(resp(c.ServeGetInternetRadioStations)))
	c.Handle("/createInternetRadioStation", chain(resp(c.ServeCreateInternetRadioStation)))
	c.Handle("/updateInternetRadioStation", chain(resp(c.ServeUpdateInternetRadioStation)))
	c.Handle("/deleteInternetRadioStation", chain(resp(c.ServeDeleteInternetRadioStation)))

	c.Handle("/", chain(resp(c.ServeNotFound)))

	return &c, nil
}

type (
	handlerSubsonic    func(r *http.Request) *spec.Response
	handlerSubsonicRaw func(w http.ResponseWriter, r *http.Request) *spec.Response
)

func resp(h handlerSubsonic) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := writeResp(w, r, h(r)); err != nil {
			log.Printf("error writing subsonic response: %v\n", err)
		}
	})
}

func respRaw(h handlerSubsonicRaw) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := writeResp(w, r, h(w, r)); err != nil {
			log.Printf("error writing raw subsonic response: %v\n", err)
		}
	})
}

func withParams(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		params := params.New(r)
		withParams := context.WithValue(r.Context(), CtxParams, params)
		next.ServeHTTP(w, r.WithContext(withParams))
	})
}

func withRequiredParams(next http.Handler) http.Handler {
	requiredParameters := []string{
		"u", "c",
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		params := r.Context().Value(CtxParams).(params.Params)
		for _, req := range requiredParameters {
			if _, err := params.Get(req); err != nil {
				_ = writeResp(w, r, spec.NewError(10, "please provide a %q parameter", req))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func withUser(dbc *db.DB) handlerutil.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			params := r.Context().Value(CtxParams).(params.Params)
			// ignoring errors here, a middleware has already ensured they exist
			username, _ := params.Get("u")
			guest_session, _ := params.Get("g")
			password, _ := params.Get("p")
			token, _ := params.Get("t")
			salt, _ := params.Get("s")

			passwordAuth := token == "" && salt == ""
			tokenAuth := password == ""
			if tokenAuth == passwordAuth {
				_ = writeResp(w, r, spec.NewError(10,
					"please provide `t` and `s`, or just `p`"))
				return
			}

			// Check if guest credentials are being used
			guestEnabled, _ := dbc.GetSetting(db.GuestEnabled)
			guestUsername, _ := dbc.GetSetting(db.GuestUsername)
			guestPassword, _ := dbc.GetSetting(db.GuestPassword)

			isGuest := false
			if guestEnabled == "true" && username == guestUsername {
				if passwordAuth && password == guestPassword {
					isGuest = true
				} else if tokenAuth {
					isGuest = checkCredsToken(guestPassword, token, salt)
				}
			}

			var user *db.User

			if isGuest {
				log.Printf("guest user %q authenticated", username)
				var guestUser db.User
				// no session, create
				if guest_session == "" {
					guest_session = randomstring.CookieFriendlyString(10)
					tempName := username + "_" + guest_session
					log.Printf("guest user %q creating", tempName)
					guestUser = db.User{
						Name:     tempName,
						Password: guestPassword,
						IsAdmin:  false,
					}
					if err := dbc.Create(&guestUser).Error; err != nil {
						_ = writeResp(w, r, spec.NewError(40, fmt.Sprintf("failed to create guest user %q", tempName)))
						return
					}
				} else {
					tempName := username + "_" + guest_session
					log.Printf("guest user %q found", tempName)
					err := dbc.Where("name == ?", tempName).First(&guestUser).Error
					if err != nil {
						_ = writeResp(w, r, spec.NewError(40, fmt.Sprintf("failed to find guest session for user %q", tempName)))
						return
					}
				}
				// Find or create a temporary guest user
				user = &guestUser
				user.GuestSession = guest_session
			} else {
				// Regular authentication flow
				user = dbc.GetUserByName(username)
				if user == nil {
					_ = writeResp(w, r, spec.NewError(40, "invalid username %q", username))
					return
				}

				var credsOk bool
				if tokenAuth {
					credsOk = checkCredsToken(user.Password, token, salt)
				} else {
					credsOk = checkCredsBasic(user.Password, password)
				}

				if !credsOk {
					_ = writeResp(w, r, spec.NewError(40, "invalid password"))
					return
				}
			}

			withUser := context.WithValue(r.Context(), CtxUser, user)
			next.ServeHTTP(w, r.WithContext(withUser))
		})
	}
}

func slow(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)  //nolint:bodyclose
		_ = rc.SetWriteDeadline(time.Time{}) // set no deadline, since we're probably streaming
		_ = rc.SetReadDeadline(time.Time{})  // set no deadline, since we're probably streaming
		next.ServeHTTP(w, r)
	})
}

func checkCredsToken(password, token, salt string) bool {
	toHash := fmt.Sprintf("%s%s", password, salt)
	hash := md5.Sum([]byte(toHash))
	expToken := hex.EncodeToString(hash[:])
	return token == expToken
}

func checkCredsBasic(password, given string) bool {
	if len(given) >= 4 && given[:4] == "enc:" {
		bytes, _ := hex.DecodeString(given[4:])
		given = string(bytes)
	}
	return password == given
}

type errWriter struct {
	w   io.Writer
	err error
}

func (ew *errWriter) write(buf []byte) {
	if ew.err != nil {
		return
	}
	_, ew.err = ew.w.Write(buf)
}

func writeResp(w http.ResponseWriter, r *http.Request, resp *spec.Response) error {
	if resp == nil {
		return nil
	}
	if resp.Error != nil {
		log.Printf("subsonic error code %d: %s", resp.Error.Code, resp.Error.Message)
	}

	user := r.Context().Value(CtxUser).(*db.User)
	if user.GuestSession != "" {
		resp.GuestSession = user.GuestSession
	}

	var res struct {
		XMLName        xml.Name `xml:"subsonic-response" json:"-"`
		*spec.Response `json:"subsonic-response"`
	}
	res.Response = resp

	params := r.Context().Value(CtxParams).(params.Params)

	ew := &errWriter{w: w}
	switch v, _ := params.Get("f"); v {
	case "json":
		w.Header().Set("Content-Type", "application/json")
		data, err := json.Marshal(res)
		if err != nil {
			return fmt.Errorf("marshal to json: %w", err)
		}
		ew.write(data)

	case "jsonp":
		w.Header().Set("Content-Type", "application/javascript")
		data, err := json.Marshal(res)
		if err != nil {
			return fmt.Errorf("marshal to jsonp: %w", err)
		}
		// TODO: error if no callback provided instead of using a default
		pCall := params.GetOr("callback", "cb")
		ew.write([]byte(pCall))
		ew.write([]byte("("))
		ew.write(data)
		ew.write([]byte(");"))

	default:
		w.Header().Set("Content-Type", "application/xml")
		data, err := xml.MarshalIndent(res, "", "    ")
		if err != nil {
			return fmt.Errorf("marshal to xml: %w", err)
		}

		ew.write(data)
	}

	return ew.err
}
