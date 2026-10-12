// Package yt is a thin layer over the YouTube Data API v3: one method per
// thing ytctl can do, with the request parameters decided in one place.
package yt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/youtube/v3"
)

// uploadChunkSize is how much of a video goes out per request. A failed
// chunk is retried on its own, so this is also the most that an unreliable
// connection can make an upload repeat.
const uploadChunkSize = 8 << 20

var (
	channelParts      = []string{"brandingSettings", "contentDetails", "id", "snippet", "statistics", "status", "topicDetails"}
	videoParts        = []string{"contentDetails", "id", "snippet", "statistics", "status", "topicDetails"}
	playlistParts     = []string{"contentDetails", "id", "snippet", "status"}
	playlistItemParts = []string{"contentDetails", "id", "snippet", "status"}
)

// Client calls the YouTube Data API as the signed-in account.
type Client struct {
	svc *youtube.Service
}

// New returns a Client that sends its requests through httpClient, which
// is expected to add the OAuth credentials. endpoint overrides the API's
// base URL and is empty outside tests.
func New(ctx context.Context, httpClient *http.Client, endpoint string) (*Client, error) {
	opts := []option.ClientOption{option.WithHTTPClient(httpClient), option.WithUserAgent("ytctl")}
	if endpoint != "" {
		opts = append(opts, option.WithEndpoint(endpoint))
	}
	svc, err := youtube.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating YouTube client: %w", err)
	}
	return &Client{svc: svc}, nil
}

// NotFoundError reports that the API answered successfully but had no
// such resource.
type NotFoundError struct {
	Kind, ID string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s %q not found", e.Kind, e.ID)
}

// MyChannel returns the signed-in account's channel.
func (c *Client) MyChannel(ctx context.Context) (*youtube.Channel, error) {
	resp, err := c.svc.Channels.List(channelParts).Mine(true).MaxResults(1).Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	if len(resp.Items) == 0 {
		return nil, errors.New("this Google account has no YouTube channel")
	}
	return resp.Items[0], nil
}

// Channel returns the channel with the given ID.
func (c *Client) Channel(ctx context.Context, id string) (*youtube.Channel, error) {
	resp, err := c.svc.Channels.List(channelParts).Id(id).Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	if len(resp.Items) == 0 {
		return nil, &NotFoundError{"channel", id}
	}
	return resp.Items[0], nil
}

// FindChannels looks a channel up by name: an @handle if name starts with
// "@", otherwise a legacy username.
func (c *Client) FindChannels(ctx context.Context, name string) ([]*youtube.Channel, error) {
	call := c.svc.Channels.List(channelParts).MaxResults(5)
	if len(name) > 0 && name[0] == '@' {
		call = call.ForHandle(name)
	} else {
		call = call.ForUsername(name)
	}
	resp, err := call.Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	return resp.Items, nil
}

// VideoSearch selects which videos a search returns. Exactly one of
// Query, ChannelID and Mine is normally set.
type VideoSearch struct {
	Query     string
	ChannelID string
	Mine      bool
	Order     string
	Limit     int64
}

// SearchVideos runs a video search. Results carry a snippet only; use
// Video for statistics. Each call costs 100 quota units.
func (c *Client) SearchVideos(ctx context.Context, s VideoSearch) ([]*youtube.SearchResult, error) {
	call := c.svc.Search.List([]string{"snippet"}).Type("video").MaxResults(s.Limit)
	if s.Query != "" {
		call = call.Q(s.Query)
	}
	if s.ChannelID != "" {
		call = call.ChannelId(s.ChannelID)
	}
	if s.Mine {
		call = call.ForMine(true)
	}
	if s.Order != "" {
		call = call.Order(s.Order)
	}
	resp, err := call.Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	return resp.Items, nil
}

// Video returns the video with the given ID.
func (c *Client) Video(ctx context.Context, id string) (*youtube.Video, error) {
	resp, err := c.svc.Videos.List(videoParts).Id(id).Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	if len(resp.Items) == 0 {
		return nil, &NotFoundError{"video", id}
	}
	return resp.Items[0], nil
}

// VideoPatch names the fields of a video to change; nil leaves a field as
// it is.
type VideoPatch struct {
	Title         *string
	Description   *string
	Tags          *[]string
	CategoryID    *string
	PrivacyStatus *string
	// PublishAt reschedules the video to an RFC 3339 time; an empty
	// string cancels the schedule and leaves the video private.
	PublishAt *string
}

// UpdateVideo changes only the fields set in patch.
//
// The API's update replaces a whole part: anything left out of "snippet"
// or "status" is reset to its default. So the current values are fetched
// first and sent back alongside the changes, and a part is only sent at
// all if the patch touches it.
func (c *Client) UpdateVideo(ctx context.Context, id string, patch VideoPatch) (*youtube.Video, error) {
	resp, err := c.svc.Videos.List([]string{"snippet", "status"}).Id(id).Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	if len(resp.Items) == 0 {
		return nil, &NotFoundError{"video", id}
	}
	cur := resp.Items[0]

	update := &youtube.Video{Id: id}
	var parts []string

	if patch.Title != nil || patch.Description != nil || patch.Tags != nil || patch.CategoryID != nil {
		// Only the writable fields are copied. In particular "localized"
		// is left behind: sent back unchanged, its old title and
		// description would override the new ones.
		s := &youtube.VideoSnippet{
			Title:                cur.Snippet.Title,
			Description:          cur.Snippet.Description,
			Tags:                 cur.Snippet.Tags,
			CategoryId:           cur.Snippet.CategoryId,
			DefaultLanguage:      cur.Snippet.DefaultLanguage,
			DefaultAudioLanguage: cur.Snippet.DefaultAudioLanguage,
		}
		if patch.Title != nil {
			s.Title = *patch.Title
		}
		if patch.Description != nil {
			s.Description = *patch.Description
		}
		if patch.Tags != nil {
			s.Tags = *patch.Tags
		}
		if patch.CategoryID != nil {
			s.CategoryId = *patch.CategoryID
		}
		// An emptied description or tag list has to be sent explicitly,
		// or it would be dropped from the JSON and so left unchanged.
		s.ForceSendFields = []string{"Description", "Tags"}
		update.Snippet = s
		parts = append(parts, "snippet")
	}

	if patch.PrivacyStatus != nil || patch.PublishAt != nil {
		privacy := cur.Status.PrivacyStatus
		if patch.PrivacyStatus != nil {
			privacy = *patch.PrivacyStatus
		}
		st := &youtube.VideoStatus{
			PrivacyStatus:           privacy,
			Embeddable:              cur.Status.Embeddable,
			License:                 cur.Status.License,
			PublicStatsViewable:     cur.Status.PublicStatsViewable,
			SelfDeclaredMadeForKids: cur.Status.SelfDeclaredMadeForKids,
			ContainsSyntheticMedia:  cur.Status.ContainsSyntheticMedia,
			ForceSendFields:         []string{"Embeddable", "PublicStatsViewable", "SelfDeclaredMadeForKids", "ContainsSyntheticMedia"},
		}
		switch {
		case patch.PublishAt != nil && *patch.PublishAt != "":
			// YouTube only schedules private videos. Quietly making a
			// live video private to schedule it would take it offline,
			// so that has to be asked for.
			if privacy != "private" {
				return nil, fmt.Errorf("video %s is %s, and only a private video can be scheduled; add --privacy private to take it offline until the scheduled time", id, privacy)
			}
			st.PublishAt = *patch.PublishAt
		case patch.PublishAt == nil && privacy == "private":
			// A scheduled video stays scheduled only while it stays
			// private.
			st.PublishAt = cur.Status.PublishAt
		}
		update.Status = st
		parts = append(parts, "status")
	}

	if len(parts) == 0 {
		return nil, errors.New("nothing to update")
	}
	out, err := c.svc.Videos.Update(parts, update).Context(ctx).Do()
	return out, explain(err)
}

// RateVideo sets the account's rating of a video: like, dislike or none.
func (c *Client) RateVideo(ctx context.Context, id, rating string) error {
	return explain(c.svc.Videos.Rate(id, rating).Context(ctx).Do())
}

// Categories lists the video categories available in a region.
func (c *Client) Categories(ctx context.Context, regionCode string) ([]*youtube.VideoCategory, error) {
	resp, err := c.svc.VideoCategories.List([]string{"snippet"}).RegionCode(regionCode).Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	return resp.Items, nil
}

// SetThumbnail uploads image as the video's custom thumbnail.
func (c *Client) SetThumbnail(ctx context.Context, videoID string, image io.Reader) error {
	_, err := c.svc.Thumbnails.Set(videoID).Media(image).Context(ctx).Do()
	return explain(err)
}

// Upload sends media as a new video described by video. The upload is
// resumable: it goes out in chunks, each retried on its own. progress, if
// not nil, is called with the number of bytes sent so far.
func (c *Client) Upload(ctx context.Context, video *youtube.Video, notifySubscribers bool, media io.Reader, progress func(sent int64)) (*youtube.Video, error) {
	call := c.svc.Videos.Insert([]string{"snippet", "status"}, video).
		NotifySubscribers(notifySubscribers).
		Media(media, googleapi.ChunkSize(uploadChunkSize))
	if progress != nil {
		call = call.ProgressUpdater(func(current, _ int64) { progress(current) })
	}
	out, err := call.Context(ctx).Do()
	return out, explain(err)
}

// Playlists lists a channel's playlists, or the signed-in account's own
// if channelID is empty.
func (c *Client) Playlists(ctx context.Context, channelID string, limit int64) ([]*youtube.Playlist, error) {
	call := c.svc.Playlists.List(playlistParts).MaxResults(limit)
	if channelID != "" {
		call = call.ChannelId(channelID)
	} else {
		call = call.Mine(true)
	}
	resp, err := call.Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	return resp.Items, nil
}

// Playlist returns the playlist with the given ID.
func (c *Client) Playlist(ctx context.Context, id string) (*youtube.Playlist, error) {
	resp, err := c.svc.Playlists.List(playlistParts).Id(id).Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	if len(resp.Items) == 0 {
		return nil, &NotFoundError{"playlist", id}
	}
	return resp.Items[0], nil
}

// CreatePlaylist creates a playlist on the signed-in account's channel.
func (c *Client) CreatePlaylist(ctx context.Context, title, description, privacyStatus string) (*youtube.Playlist, error) {
	out, err := c.svc.Playlists.Insert([]string{"snippet", "status"}, &youtube.Playlist{
		Snippet: &youtube.PlaylistSnippet{Title: title, Description: description},
		Status:  &youtube.PlaylistStatus{PrivacyStatus: privacyStatus},
	}).Context(ctx).Do()
	return out, explain(err)
}

// PlaylistPatch names the fields of a playlist to change; nil leaves a
// field as it is.
type PlaylistPatch struct {
	Title         *string
	Description   *string
	PrivacyStatus *string
}

// UpdatePlaylist changes only the fields set in patch, fetching the
// current values first for the same reason UpdateVideo does.
func (c *Client) UpdatePlaylist(ctx context.Context, id string, patch PlaylistPatch) (*youtube.Playlist, error) {
	if patch.Title == nil && patch.Description == nil && patch.PrivacyStatus == nil {
		return nil, errors.New("nothing to update")
	}
	resp, err := c.svc.Playlists.List([]string{"snippet", "status"}).Id(id).Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	if len(resp.Items) == 0 {
		return nil, &NotFoundError{"playlist", id}
	}
	cur := resp.Items[0]

	update := &youtube.Playlist{
		Id: id,
		Snippet: &youtube.PlaylistSnippet{
			Title:           cur.Snippet.Title,
			Description:     cur.Snippet.Description,
			DefaultLanguage: cur.Snippet.DefaultLanguage,
			ForceSendFields: []string{"Description"},
		},
		Status: &youtube.PlaylistStatus{PrivacyStatus: cur.Status.PrivacyStatus},
	}
	if patch.Title != nil {
		update.Snippet.Title = *patch.Title
	}
	if patch.Description != nil {
		update.Snippet.Description = *patch.Description
	}
	if patch.PrivacyStatus != nil {
		update.Status.PrivacyStatus = *patch.PrivacyStatus
	}

	out, err := c.svc.Playlists.Update([]string{"snippet", "status"}, update).Context(ctx).Do()
	return out, explain(err)
}

// DeletePlaylist deletes a playlist. The videos in it are not affected.
func (c *Client) DeletePlaylist(ctx context.Context, id string) error {
	return explain(c.svc.Playlists.Delete(id).Context(ctx).Do())
}

// PlaylistItems lists the entries of a playlist.
func (c *Client) PlaylistItems(ctx context.Context, playlistID string, limit int64) ([]*youtube.PlaylistItem, error) {
	resp, err := c.svc.PlaylistItems.List(playlistItemParts).PlaylistId(playlistID).MaxResults(limit).Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	return resp.Items, nil
}

// AddToPlaylist appends a video to a playlist.
func (c *Client) AddToPlaylist(ctx context.Context, playlistID, videoID string) (*youtube.PlaylistItem, error) {
	out, err := c.svc.PlaylistItems.Insert([]string{"snippet"}, &youtube.PlaylistItem{
		Snippet: &youtube.PlaylistItemSnippet{
			PlaylistId: playlistID,
			ResourceId: &youtube.ResourceId{Kind: "youtube#video", VideoId: videoID},
		},
	}).Context(ctx).Do()
	return out, explain(err)
}

// RemoveFromPlaylist removes one entry from a playlist. It takes the
// entry's own ID, as listed by PlaylistItems, not the video's.
func (c *Client) RemoveFromPlaylist(ctx context.Context, playlistItemID string) error {
	return explain(c.svc.PlaylistItems.Delete(playlistItemID).Context(ctx).Do())
}

// CommentThreads lists a video's top-level comments with their replies.
func (c *Client) CommentThreads(ctx context.Context, videoID, order string, limit int64) ([]*youtube.CommentThread, error) {
	resp, err := c.svc.CommentThreads.List([]string{"snippet", "replies"}).
		VideoId(videoID).MaxResults(limit).Order(order).TextFormat("plainText").Context(ctx).Do()
	if err != nil {
		return nil, explain(err)
	}
	return resp.Items, nil
}

// PostComment posts a new top-level comment on a video.
func (c *Client) PostComment(ctx context.Context, videoID, text string) (*youtube.CommentThread, error) {
	out, err := c.svc.CommentThreads.Insert([]string{"snippet"}, &youtube.CommentThread{
		Snippet: &youtube.CommentThreadSnippet{
			VideoId:         videoID,
			TopLevelComment: &youtube.Comment{Snippet: &youtube.CommentSnippet{TextOriginal: text}},
		},
	}).Context(ctx).Do()
	return out, explain(err)
}

// ReplyToComment replies to a comment thread.
func (c *Client) ReplyToComment(ctx context.Context, threadID, text string) (*youtube.Comment, error) {
	out, err := c.svc.Comments.Insert([]string{"snippet"}, &youtube.Comment{
		Snippet: &youtube.CommentSnippet{ParentId: threadID, TextOriginal: text},
	}).Context(ctx).Do()
	return out, explain(err)
}
