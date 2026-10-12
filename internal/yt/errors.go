package yt

import (
	"errors"
	"fmt"
	"html"
	"regexp"

	"google.golang.org/api/googleapi"
)

// hints explain the API error reasons a user can actually do something
// about. The API's own messages for these are either terse or misleading.
var hints = map[string]string{
	"quotaExceeded":                         "the Google Cloud project has used up today's YouTube API quota. By default uploads and searches are each limited to 100 calls a day, and everything else shares 10,000 units a day; all of it resets at midnight Pacific time",
	"uploadLimitExceeded":                   "the channel has reached YouTube's limit on uploads for now; try again in a day",
	"youtubeSignupRequired":                 "this Google account has no YouTube channel yet; create one at youtube.com first",
	"insufficientPermissions":               "the stored sign-in does not cover this action; run `ytctl auth login` again and grant every permission it asks for",
	"commentsDisabled":                      "comments are turned off for this video",
	"forbidden":                             "the signed-in account is not allowed to do this; it can only change its own channel's videos, playlists and comments",
	"accessNotConfigured":                   "the YouTube Data API v3 is not enabled for the Google Cloud project that owns the OAuth client; enable it in the Cloud console",
	"invalidPublishAt":                      "the scheduled publish time was rejected; it must be in the future",
	"playlistContainsMaximumNumberOfVideos": "the playlist is full",
}

// explain rewrites an API error into one line a user can act on, keeping
// the original available to errors.As.
func explain(err error) error {
	if err == nil {
		return nil
	}
	var gerr *googleapi.Error
	if !errors.As(err, &gerr) {
		return err
	}

	reason := ""
	if len(gerr.Errors) > 0 {
		reason = gerr.Errors[0].Reason
	}
	if hint, ok := hints[reason]; ok {
		return fmt.Errorf("%s (%s): %w", hint, reason, &apiError{gerr})
	}
	return &apiError{gerr}
}

// apiError prints a googleapi.Error without the library's multi-line
// "Error 403: ..., reason\nMore details: ..." dump.
type apiError struct {
	err *googleapi.Error
}

// htmlTag matches the markup the API puts in its messages, which link
// to its reference pages.
var htmlTag = regexp.MustCompile(`<[^>]+>`)

func (e *apiError) Error() string {
	msg := html.UnescapeString(htmlTag.ReplaceAllString(e.err.Message, ""))
	if msg == "" {
		msg = "no message"
	}
	return fmt.Sprintf("YouTube API error %d: %s", e.err.Code, msg)
}

func (e *apiError) Unwrap() error { return e.err }
