package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/orshih6/ytctl/internal/yt"
)

// request is one call the fake API received.
type request struct {
	Method string
	Path   string
	Query  map[string]string
	Body   map[string]any // JSON body, or the JSON part of a media upload
	Media  []byte         // the media part of an upload
}

// fakeAPI stands in for the YouTube Data API. Responses are canned per
// "METHOD /path"; anything else answers with an empty JSON object.
type fakeAPI struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	requests  []request
	responses map[string]string
	failures  map[string]string // route -> error reason, answered as 403
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{t: t, responses: map[string]string{}, failures: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)

	old := newClient
	newClient = func(cmd *cobra.Command) (*yt.Client, error) {
		return yt.New(cmd.Context(), f.srv.Client(), f.srv.URL)
	}
	t.Cleanup(func() { newClient = old })
	return f
}

func (f *fakeAPI) handle(w http.ResponseWriter, r *http.Request) {
	req := request{Method: r.Method, Path: r.URL.Path, Query: map[string]string{}}
	for k, v := range r.URL.Query() {
		req.Query[k] = strings.Join(v, ",")
	}

	mediaType, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(r.Body, params["boundary"])
		for i := 0; ; i++ {
			part, err := mr.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(part)
			if i == 0 {
				json.Unmarshal(data, &req.Body)
			} else {
				req.Media = data
			}
		}
	} else if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		if json.Unmarshal(data, &req.Body) != nil {
			req.Media = data
		}
	}

	f.mu.Lock()
	f.requests = append(f.requests, req)
	route := r.Method + " " + r.URL.Path
	body, ok := f.responses[route]
	reason, fail := f.failures[route]
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if fail {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
			"code": 403, "message": "the API's own message",
			"errors": []map[string]string{{"reason": reason, "message": "the API's own message"}},
		}})
		return
	}
	if !ok {
		body = "{}"
	}
	if r.Method == http.MethodDelete || strings.HasSuffix(r.URL.Path, "/rate") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	io.WriteString(w, body)
}

// only returns the single request made to route, failing the test if
// there was not exactly one.
func (f *fakeAPI) only(route string) request {
	f.t.Helper()
	var found []request
	for _, r := range f.requests {
		if r.Method+" "+r.Path == route {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		f.t.Fatalf("got %d requests to %q, want 1; all requests: %v", len(found), route, f.routes())
	}
	return found[0]
}

func (f *fakeAPI) routes() []string {
	var out []string
	for _, r := range f.requests {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// run executes ytctl with args against an empty config, feeding it stdin.
func run(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	for _, k := range []string{"YTCTL_CLIENT_ID", "YTCTL_CLIENT_SECRET", "YTCTL_CLIENT_SECRET_FILE", "YTCTL_TOKEN_FILE"} {
		t.Setenv(k, "")
	}
	var out, errOut bytes.Buffer
	cmd := newRootCommand()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{"--config", filepath.Join(t.TempDir(), "config.yaml")}, args...))
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func wantQuery(t *testing.T, req request, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got, ok := req.Query[k]; !ok || got != v {
			t.Errorf("%s %s: query %s = %q (present: %v), want %q", req.Method, req.Path, k, got, ok, v)
		}
	}
}

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[p]
	}
	return cur
}

func TestChannelMine(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["GET /youtube/v3/channels"] = `{"items":[{"id":"UC123","snippet":{"title":"Kettle Facts"},"statistics":{"subscriberCount":"42","videoCount":"7"}}]}`

	out, _, err := run(t, "", "channel", "mine")
	if err != nil {
		t.Fatal(err)
	}
	wantQuery(t, api.only("GET /youtube/v3/channels"), map[string]string{"mine": "true", "maxResults": "1"})
	for _, want := range []string{"UC123", "Kettle Facts", "42"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestChannelFindUsesHandleOrUsername(t *testing.T) {
	api := newFakeAPI(t)
	if _, _, err := run(t, "", "channel", "find", "@kettle"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "", "channel", "find", "kettle"); err != nil {
		t.Fatal(err)
	}
	if got := api.requests[0].Query["forHandle"]; got != "@kettle" {
		t.Errorf("forHandle = %q", got)
	}
	if got := api.requests[1].Query["forUsername"]; got != "kettle" {
		t.Errorf("forUsername = %q", got)
	}
}

func TestVideoSearchAndList(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["GET /youtube/v3/search"] = `{"items":[{"id":{"videoId":"abcdefghijk"},"snippet":{"title":"How kettles work","channelTitle":"Kettle Facts","publishedAt":"2026-01-02T03:04:05Z"}}]}`

	out, _, err := run(t, "", "video", "search", "kettles", "--limit", "5", "--order", "viewCount")
	if err != nil {
		t.Fatal(err)
	}
	wantQuery(t, api.requests[0], map[string]string{"q": "kettles", "type": "video", "maxResults": "5", "order": "viewCount", "part": "snippet"})
	if !strings.Contains(out, "abcdefghijk") || !strings.Contains(out, "2026-01-02") {
		t.Errorf("unexpected table:\n%s", out)
	}

	if _, _, err := run(t, "", "video", "list"); err != nil {
		t.Fatal(err)
	}
	wantQuery(t, api.requests[1], map[string]string{"forMine": "true", "type": "video", "maxResults": "25", "order": "date"})
	if _, ok := api.requests[1].Query["channelId"]; ok {
		t.Error("listing own videos sent a channelId")
	}

	if _, _, err := run(t, "", "video", "list", "--channel", "UC999"); err != nil {
		t.Fatal(err)
	}
	wantQuery(t, api.requests[2], map[string]string{"channelId": "UC999", "type": "video"})
	if _, ok := api.requests[2].Query["forMine"]; ok {
		t.Error("listing another channel's videos sent forMine")
	}
}

func TestInvalidFlagsMakeNoRequest(t *testing.T) {
	api := newFakeAPI(t)
	for _, args := range [][]string{
		{"video", "search", "x", "--limit", "51"},
		{"video", "search", "x", "--order", "newest"},
		{"comment", "list", "vid", "--limit", "0"},
		{"playlist", "create", "x", "--privacy", "secret"},
		{"video", "update", "vid"},
		{"video", "update", "vid", "--title", strings.Repeat("x", 101)},
		{"video", "rate", "vid", "love"},
	} {
		if _, _, err := run(t, "", args...); err == nil {
			t.Errorf("ytctl %s succeeded, want an error", strings.Join(args, " "))
		}
	}
	if len(api.requests) != 0 {
		t.Errorf("invalid input still reached the API: %v", api.routes())
	}
}

const currentVideo = `{"items":[{"id":"vid","snippet":{
	"title":"Old title","description":"Old description","tags":["a","b"],"categoryId":"27",
	"defaultLanguage":"en","channelId":"UC1","publishedAt":"2026-01-01T00:00:00Z",
	"localized":{"title":"Old title","description":"Old description"}},
	"status":{"privacyStatus":"private","publishAt":"2030-01-01T00:00:00Z","embeddable":false,
	"license":"creativeCommon","publicStatsViewable":true,"selfDeclaredMadeForKids":true,"uploadStatus":"processed"}}]}`

func TestVideoUpdateKeepsWhatWasNotChanged(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["GET /youtube/v3/videos"] = currentVideo

	if _, _, err := run(t, "", "video", "update", "vid", "--title", "New title"); err != nil {
		t.Fatal(err)
	}

	put := api.only("PUT /youtube/v3/videos")
	wantQuery(t, put, map[string]string{"part": "snippet"})
	want := map[string]any{"title": "New title", "description": "Old description", "categoryId": "27", "defaultLanguage": "en"}
	for k, v := range want {
		if got := dig(put.Body, "snippet", k); got != v {
			t.Errorf("snippet.%s = %v, want %v", k, got, v)
		}
	}
	if tags, _ := dig(put.Body, "snippet", "tags").([]any); len(tags) != 2 {
		t.Errorf("snippet.tags = %v, want the two existing tags", tags)
	}
	if dig(put.Body, "snippet", "localized") != nil {
		t.Error("snippet.localized was sent back; its old title would override the new one")
	}
	if dig(put.Body, "status") != nil {
		t.Error("status was sent although no status flag was given")
	}
	if dig(put.Body, "id") != "vid" {
		t.Errorf("id = %v", dig(put.Body, "id"))
	}
}

func TestVideoUpdateCanClearTagsAndDescription(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["GET /youtube/v3/videos"] = currentVideo

	if _, _, err := run(t, "", "video", "update", "vid", "--tags", "", "--description", ""); err != nil {
		t.Fatal(err)
	}
	put := api.only("PUT /youtube/v3/videos")
	if tags, ok := dig(put.Body, "snippet", "tags").([]any); !ok || len(tags) != 0 {
		t.Errorf("snippet.tags = %#v, want an explicit empty list", dig(put.Body, "snippet", "tags"))
	}
	if got, ok := dig(put.Body, "snippet", "description").(string); !ok || got != "" {
		t.Errorf("snippet.description = %#v, want an explicit empty string", dig(put.Body, "snippet", "description"))
	}
	if dig(put.Body, "snippet", "title") != "Old title" {
		t.Error("the title was not kept")
	}
}

func TestVideoUpdatePrivacyKeepsOtherStatusFields(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["GET /youtube/v3/videos"] = currentVideo

	if _, _, err := run(t, "", "video", "update", "vid", "--privacy", "unlisted"); err != nil {
		t.Fatal(err)
	}
	put := api.only("PUT /youtube/v3/videos")
	wantQuery(t, put, map[string]string{"part": "status"})
	if dig(put.Body, "snippet") != nil {
		t.Error("snippet was sent although no snippet flag was given")
	}
	want := map[string]any{"privacyStatus": "unlisted", "embeddable": false, "license": "creativeCommon", "publicStatsViewable": true, "selfDeclaredMadeForKids": true}
	for k, v := range want {
		if got := dig(put.Body, "status", k); got != v {
			t.Errorf("status.%s = %#v, want %#v", k, got, v)
		}
	}
	if dig(put.Body, "status", "publishAt") != nil {
		t.Error("publishAt was kept on a video that is no longer private")
	}
	if dig(put.Body, "status", "uploadStatus") != nil {
		t.Error("read-only uploadStatus was sent back")
	}
}

func TestVideoUpdateMissingVideo(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["GET /youtube/v3/videos"] = `{"items":[]}`

	_, _, err := run(t, "", "video", "update", "nope", "--title", "x")
	if err == nil || !strings.Contains(err.Error(), `video "nope" not found`) {
		t.Errorf("err = %v", err)
	}
	if len(api.requests) != 1 {
		t.Errorf("requests = %v, want only the lookup", api.routes())
	}
}

func TestPlaylistUpdateKeepsWhatWasNotChanged(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["GET /youtube/v3/playlists"] = `{"items":[{"id":"PL1","snippet":{"title":"Old","description":"Kept"},"status":{"privacyStatus":"public"}}]}`

	if _, _, err := run(t, "", "playlist", "update", "PL1", "--title", "New"); err != nil {
		t.Fatal(err)
	}
	put := api.only("PUT /youtube/v3/playlists")
	if dig(put.Body, "snippet", "title") != "New" || dig(put.Body, "snippet", "description") != "Kept" || dig(put.Body, "status", "privacyStatus") != "public" {
		t.Errorf("body = %v", put.Body)
	}
}

func TestPlaylistDeleteAsksFirst(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["GET /youtube/v3/playlists"] = `{"items":[{"id":"PL1","snippet":{"title":"Favourites"}}]}`

	_, stderr, err := run(t, "n\n", "playlist", "delete", "PL1")
	if err == nil {
		t.Error("declined delete reported success")
	}
	if !strings.Contains(stderr, "Favourites") {
		t.Errorf("the question does not name the playlist: %q", stderr)
	}
	for _, r := range api.routes() {
		if strings.HasPrefix(r, "DELETE") {
			t.Fatal("playlist was deleted after answering no")
		}
	}

	if _, _, err := run(t, "", "playlist", "delete", "PL1"); err == nil {
		t.Error("delete with no answer at all reported success")
	}
	if _, _, err := run(t, "y\n", "playlist", "delete", "PL1"); err != nil {
		t.Fatal(err)
	}
	wantQuery(t, api.only("DELETE /youtube/v3/playlists"), map[string]string{"id": "PL1"})
}

func TestPlaylistItemsAddRemove(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["POST /youtube/v3/playlistItems"] = `{"id":"ITEM1"}`

	out, _, err := run(t, "", "playlist", "add", "PL1", "vid")
	if err != nil {
		t.Fatal(err)
	}
	post := api.only("POST /youtube/v3/playlistItems")
	if dig(post.Body, "snippet", "playlistId") != "PL1" || dig(post.Body, "snippet", "resourceId", "videoId") != "vid" || dig(post.Body, "snippet", "resourceId", "kind") != "youtube#video" {
		t.Errorf("body = %v", post.Body)
	}
	if !strings.Contains(out, "ITEM1") {
		t.Errorf("output does not give the new item's ID: %q", out)
	}

	if _, _, err := run(t, "", "playlist", "remove", "ITEM1", "--yes"); err != nil {
		t.Fatal(err)
	}
	wantQuery(t, api.only("DELETE /youtube/v3/playlistItems"), map[string]string{"id": "ITEM1"})

	if _, _, err := run(t, "", "playlist", "items", "PL1", "--limit", "3"); err != nil {
		t.Fatal(err)
	}
	wantQuery(t, api.only("GET /youtube/v3/playlistItems"), map[string]string{"playlistId": "PL1", "maxResults": "3"})
}

func TestComments(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["GET /youtube/v3/commentThreads"] = `{"items":[{"id":"THREAD1","snippet":{"topLevelComment":{"snippet":{"authorDisplayName":"Ann","textDisplay":"Nice","publishedAt":"2026-02-03T00:00:00Z","likeCount":2}}},
		"replies":{"comments":[{"snippet":{"authorDisplayName":"Bob","textDisplay":"Agreed","publishedAt":"2026-02-04T00:00:00Z"}}]}}]}`

	out, _, err := run(t, "", "comment", "list", "vid")
	if err != nil {
		t.Fatal(err)
	}
	wantQuery(t, api.only("GET /youtube/v3/commentThreads"), map[string]string{"videoId": "vid", "maxResults": "20", "order": "time", "textFormat": "plainText", "part": "snippet,replies"})
	for _, want := range []string{"THREAD1", "Ann", "Nice", "Bob", "Agreed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	if _, _, err := run(t, "", "comment", "post", "vid", "Hello"); err != nil {
		t.Fatal(err)
	}
	post := api.only("POST /youtube/v3/commentThreads")
	if dig(post.Body, "snippet", "videoId") != "vid" || dig(post.Body, "snippet", "topLevelComment", "snippet", "textOriginal") != "Hello" {
		t.Errorf("body = %v", post.Body)
	}

	if _, _, err := run(t, "", "comment", "reply", "THREAD1", "Thanks"); err != nil {
		t.Fatal(err)
	}
	reply := api.only("POST /youtube/v3/comments")
	if dig(reply.Body, "snippet", "parentId") != "THREAD1" || dig(reply.Body, "snippet", "textOriginal") != "Thanks" {
		t.Errorf("body = %v", reply.Body)
	}
}

func TestJSONOutput(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["GET /youtube/v3/videos"] = `{"items":[{"id":"vid","snippet":{"title":"T <3"}}]}`

	out, _, err := run(t, "", "video", "get", "vid", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if dig(v, "snippet", "title") != "T <3" {
		t.Errorf("title = %v", dig(v, "snippet", "title"))
	}

	out, _, err = run(t, "", "playlist", "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("empty list printed %q, want []", out)
	}
}

func TestQuotaErrorIsExplained(t *testing.T) {
	api := newFakeAPI(t)
	api.failures["GET /youtube/v3/search"] = "quotaExceeded"

	_, _, err := run(t, "", "video", "search", "x")
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"10,000", "1,600", "quotaExceeded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("error spans several lines: %q", err)
	}
}

func TestBuildUpload(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	base := uploadOptions{Title: "T", Privacy: "private", Category: "28"}

	with := func(change func(*uploadOptions)) uploadOptions {
		o := base
		change(&o)
		return o
	}

	t.Run("defaults", func(t *testing.T) {
		v, err := buildUpload(base, now)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status.PrivacyStatus != "private" || v.Snippet.CategoryId != "28" || v.Status.PublishAt != "" {
			t.Errorf("video = %+v / %+v", v.Snippet, v.Status)
		}
		data, _ := json.Marshal(v)
		if !strings.Contains(string(data), `"selfDeclaredMadeForKids":false`) {
			t.Errorf("made-for-kids=false is not stated explicitly: %s", data)
		}
	})

	t.Run("publish-at forces private and is sent in UTC", func(t *testing.T) {
		v, err := buildUpload(with(func(o *uploadOptions) { o.Privacy = "public"; o.PublishAt = "2026-06-02T09:00:00+08:00" }), now)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status.PrivacyStatus != "private" || v.Status.PublishAt != "2026-06-02T01:00:00Z" {
			t.Errorf("status = %+v", v.Status)
		}
	})

	for name, opts := range map[string]uploadOptions{
		"no title":             with(func(o *uploadOptions) { o.Title = "  " }),
		"title too long":       with(func(o *uploadOptions) { o.Title = strings.Repeat("x", 101) }),
		"title with brackets":  with(func(o *uploadOptions) { o.Title = "a <b>" }),
		"description too long": with(func(o *uploadOptions) { o.Description = strings.Repeat("x", 5001) }),
		"bad privacy":          with(func(o *uploadOptions) { o.Privacy = "hidden" }),
		"publish-at in past":   with(func(o *uploadOptions) { o.PublishAt = "2026-06-01T11:00:00Z" }),
		"publish-at no offset": with(func(o *uploadOptions) { o.PublishAt = "2026-06-02 09:00" }),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := buildUpload(opts, now); err == nil {
				t.Error("no error")
			}
		})
	}

	t.Run("100 non-ASCII characters is not too long", func(t *testing.T) {
		if _, err := buildUpload(with(func(o *uploadOptions) { o.Title = strings.Repeat("ө", 100) }), now); err != nil {
			t.Error(err)
		}
	})
}

func uploadFiles(t *testing.T) (video, metaFile, thumb string) {
	t.Helper()
	dir := t.TempDir()
	video = filepath.Join(dir, "v.mp4")
	metaFile = filepath.Join(dir, "youtube.md")
	thumb = filepath.Join(dir, "t.png")
	files := map[string]string{
		video:    "not really an mp4",
		metaFile: "# Upload\n\n## Title\nMeta title #Shorts\n\n## Description\nLine one\n\nLine two\n\n## Tags\nx, y\n",
		thumb:    "\x89PNG\r\n\x1a\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return video, metaFile, thumb
}

func TestUpload(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["POST /upload/youtube/v3/videos"] = `{"id":"newvid","status":{"privacyStatus":"private"}}`
	video, metaFile, thumb := uploadFiles(t)

	out, _, err := run(t, "", "upload", video, "--meta", metaFile, "--tags", "flag-tag", "--thumbnail", thumb, "--playlist", "PL1")
	if err != nil {
		t.Fatal(err)
	}

	up := api.only("POST /upload/youtube/v3/videos")
	wantQuery(t, up, map[string]string{"part": "snippet,status", "notifySubscribers": "false"})
	if string(up.Media) != "not really an mp4" {
		t.Errorf("uploaded bytes = %q", up.Media)
	}
	if dig(up.Body, "snippet", "title") != "Meta title #Shorts" || dig(up.Body, "snippet", "description") != "Line one\n\nLine two" {
		t.Errorf("snippet = %v", dig(up.Body, "snippet"))
	}
	if tags, _ := dig(up.Body, "snippet", "tags").([]any); len(tags) != 1 || tags[0] != "flag-tag" {
		t.Errorf("tags = %v, want the --tags flag to override the file", tags)
	}
	if dig(up.Body, "status", "privacyStatus") != "private" || dig(up.Body, "snippet", "categoryId") != "28" {
		t.Errorf("body = %v", up.Body)
	}

	wantQuery(t, api.only("POST /upload/youtube/v3/thumbnails/set"), map[string]string{"videoId": "newvid"})
	item := api.only("POST /youtube/v3/playlistItems")
	if dig(item.Body, "snippet", "resourceId", "videoId") != "newvid" {
		t.Errorf("playlist item = %v", item.Body)
	}
	if !strings.Contains(out, "newvid") || !strings.Contains(out, "https://youtu.be/newvid") {
		t.Errorf("output = %q", out)
	}
}

func TestUploadReportsVideoWhenFollowUpFails(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["POST /upload/youtube/v3/videos"] = `{"id":"newvid","status":{"privacyStatus":"private"}}`
	api.failures["POST /youtube/v3/playlistItems"] = "playlistNotFound"
	video, _, _ := uploadFiles(t)

	out, _, err := run(t, "", "upload", video, "--title", "T", "--playlist", "PLgone", "--json")
	if err == nil || !strings.Contains(err.Error(), "newvid") || !strings.Contains(err.Error(), "PLgone") {
		t.Errorf("err = %v, want one naming the uploaded video and the playlist", err)
	}
	var res uploadResult
	if jerr := json.Unmarshal([]byte(out), &res); jerr != nil || res.VideoID != "newvid" || res.PlaylistID != "" {
		t.Errorf("stdout = %q (%v), want the upload result without a playlist", out, jerr)
	}
}

func TestUploadNotesWhenYouTubeOverridesPrivacy(t *testing.T) {
	api := newFakeAPI(t)
	api.responses["POST /upload/youtube/v3/videos"] = `{"id":"newvid","status":{"privacyStatus":"private"}}`
	video, _, _ := uploadFiles(t)

	out, stderr, err := run(t, "", "upload", video, "--title", "T", "--privacy", "public")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Privacy: private") || !strings.Contains(stderr, "audit") {
		t.Errorf("stdout = %q\nstderr = %q", out, stderr)
	}
}

func TestUploadChecksLocalFilesBeforeUploading(t *testing.T) {
	api := newFakeAPI(t)
	video, _, _ := uploadFiles(t)
	missing := filepath.Join(t.TempDir(), "absent")

	for name, args := range map[string][]string{
		"missing video":     {"upload", missing, "--title", "T"},
		"missing thumbnail": {"upload", video, "--title", "T", "--thumbnail", missing},
		"missing meta":      {"upload", video, "--meta", missing},
		"no title anywhere": {"upload", video},
	} {
		if _, _, err := run(t, "", args...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if len(api.requests) != 0 {
		t.Errorf("a doomed upload still reached the API: %v", api.routes())
	}
}
