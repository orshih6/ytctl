package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/api/youtube/v3"

	"github.com/orshih6/ytctl/internal/meta"
)

// defaultCategory is "Science & Technology".
const defaultCategory = "28"

// uploadOptions is what the upload flags (and a --meta file) add up to.
type uploadOptions struct {
	Title       string
	Description string
	Tags        []string
	Privacy     string
	Category    string
	PublishAt   string
	MadeForKids bool
	// SyntheticMedia discloses realistic altered or AI-generated content.
	SyntheticMedia bool
}

// uploadResult is what `upload --json` prints.
type uploadResult struct {
	VideoID       string `json:"videoId"`
	URL           string `json:"url"`
	PrivacyStatus string `json:"privacyStatus"`
	PublishAt     string `json:"publishAt,omitempty"`
	Thumbnail     string `json:"thumbnail,omitempty"`
	PlaylistID    string `json:"playlistId,omitempty"`
}

func newUploadCommand() *cobra.Command {
	var (
		opts      uploadOptions
		tags      string
		metaPath  string
		thumbnail string
		playlist  string
		notify    bool
	)

	cmd := &cobra.Command{
		Use:   "upload <video-file | url>",
		Short: "Upload a video",
		Long: "Upload a video from a local file, or from an http(s) URL that serves the\n" +
			"file itself: it is streamed to YouTube without being saved first. The video\n" +
			"is uploaded as private unless --privacy says otherwise.\n\n" +
			"--meta reads the title, description and tags from a Markdown file with\n" +
			"\"## Title\", \"## Description\" and \"## Tags\" sections; a flag given as\n" +
			"well overrides that field.\n\n" +
			"--publish-at schedules the video: YouTube only schedules private videos,\n" +
			"so it is uploaded as private and goes public at that time.",
		Example: "  ytctl upload out/video.mp4 --meta youtube.md\n" +
			"  ytctl upload https://example.com/files/video.mp4 --title \"From a link\"\n" +
			"  ytctl upload clip.mp4 --title \"My clip #Shorts\" --tags a,b --publish-at 2030-01-31T09:00:00+08:00",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags := cmd.Flags()
			if metaPath != "" {
				m, err := meta.ParseFile(metaPath)
				if err != nil {
					return err
				}
				if !flags.Changed("title") {
					opts.Title = m.Title
				}
				if !flags.Changed("description") {
					opts.Description = m.Description
				}
				if !flags.Changed("tags") {
					opts.Tags = m.Tags
				}
			}
			if flags.Changed("tags") {
				opts.Tags = meta.SplitTags(tags)
			}

			video, err := buildUpload(opts, time.Now())
			if err != nil {
				return err
			}
			stderr := progressWriter(cmd)
			if opts.PublishAt != "" && flags.Changed("privacy") && opts.Privacy != "private" {
				fmt.Fprintf(stderr, "Note: --publish-at uploads as private; the video becomes public at %s.\n", video.Status.PublishAt)
			}

			// Everything that can be checked locally is checked before the
			// upload, which is the slow and quota-expensive step.
			var thumb *os.File
			if thumbnail != "" {
				if thumb, err = os.Open(thumbnail); err != nil {
					return err
				}
				defer thumb.Close()
			}
			source, size, err := openSource(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			defer source.Close()

			client, err := newClient(cmd)
			if err != nil {
				return err
			}

			progress := newProgress(stderr, size)
			uploaded, err := client.Upload(cmd.Context(), video, notify, source, progress.update)
			progress.finish()
			if err != nil {
				return fmt.Errorf("upload failed: %w", err)
			}

			result := uploadResult{
				VideoID:       uploaded.Id,
				URL:           "https://youtu.be/" + uploaded.Id,
				PrivacyStatus: video.Status.PrivacyStatus,
			}
			if uploaded.Status != nil {
				result.PrivacyStatus = uploaded.Status.PrivacyStatus
				result.PublishAt = uploaded.Status.PublishAt
			}

			// The video exists from here on. A failed follow-up step is
			// reported, but after the result, so the ID is never lost.
			var followUp []error
			if thumb != nil {
				if err := client.SetThumbnail(cmd.Context(), uploaded.Id, thumb); err != nil {
					followUp = append(followUp, fmt.Errorf("setting the thumbnail failed: %w", err))
				} else {
					result.Thumbnail = thumbnail
				}
			}
			if playlist != "" {
				if _, err := client.AddToPlaylist(cmd.Context(), playlist, uploaded.Id); err != nil {
					followUp = append(followUp, fmt.Errorf("adding to playlist %s failed: %w", playlist, err))
				} else {
					result.PlaylistID = playlist
				}
			}

			if err := emit(cmd, result, func(w io.Writer) error {
				fmt.Fprintf(w, "Uploaded %s\n%s\nPrivacy: %s\n", result.VideoID, result.URL, result.PrivacyStatus)
				if result.PublishAt != "" {
					fmt.Fprintf(w, "Scheduled for: %s\n", result.PublishAt)
				}
				return nil
			}); err != nil {
				return err
			}
			if result.PrivacyStatus != video.Status.PrivacyStatus {
				fmt.Fprintf(cmd.ErrOrStderr(), "Note: asked for %s but YouTube made the video %s. YouTube can override the privacy of an upload, for example from the channel's upload defaults; change it with: ytctl video update %s --privacy %s\n", video.Status.PrivacyStatus, result.PrivacyStatus, result.VideoID, video.Status.PrivacyStatus)
			}
			if len(followUp) > 0 {
				return fmt.Errorf("the video was uploaded as %s, but %w", uploaded.Id, errors.Join(followUp...))
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.Title, "title", "", "video title, at most 100 characters")
	f.StringVar(&opts.Description, "description", "", "video description")
	f.StringVar(&tags, "tags", "", "comma-separated tags")
	f.StringVar(&metaPath, "meta", "", "Markdown file to read the title, description and tags from")
	f.StringVar(&opts.Privacy, "privacy", "private", "privacy: "+strings.Join(privacyStatuses, ", "))
	f.StringVar(&opts.Category, "category", defaultCategory, "category ID (list them with: ytctl video categories)")
	f.StringVar(&opts.PublishAt, "publish-at", "", "schedule publication, e.g. 2030-01-31T09:00:00+08:00")
	f.BoolVar(&opts.MadeForKids, "made-for-kids", false, "declare the video as made for kids")
	f.BoolVar(&opts.SyntheticMedia, "synthetic-media", false, "disclose that the video contains realistic altered or AI-generated content")
	f.BoolVar(&notify, "notify-subscribers", false, "notify the channel's subscribers about the upload")
	f.StringVar(&thumbnail, "thumbnail", "", "JPEG or PNG (at most 50 MB) to set as the thumbnail after the upload")
	f.StringVar(&playlist, "playlist", "", "playlist ID to add the video to after the upload")
	return cmd
}

// openSource opens the video to upload: a local file, or the body of an
// http(s) URL. size is -1 when a server does not say how much is coming.
func openSource(ctx context.Context, source string) (rc io.ReadCloser, size int64, err error) {
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		file, err := os.Open(source)
		if err != nil {
			return nil, 0, err
		}
		info, err := file.Stat()
		if err == nil && (info.IsDir() || info.Size() == 0) {
			err = fmt.Errorf("%s is not a video file", source)
		}
		if err != nil {
			file.Close()
			return nil, 0, err
		}
		return file, info.Size(), nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid URL %q: %w", source, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching the video: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("fetching the video: the server answered %s", resp.Status)
	}
	// A share link to a page about the video, rather than to the file, is
	// the usual mistake. Catching it here saves sending YouTube a web page.
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/") || strings.HasPrefix(ct, "application/json") {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("%s serves a web page (%s), not a video file; use a direct link to the file", source, ct)
	}
	return resp.Body, resp.ContentLength, nil
}

// buildUpload validates the options and turns them into the resource to
// insert. now is the time to judge --publish-at against.
func buildUpload(opts uploadOptions, now time.Time) (*youtube.Video, error) {
	if strings.TrimSpace(opts.Title) == "" {
		return nil, errors.New("a title is required: pass --title, or --meta with a \"## Title\" section")
	}
	if err := checkTitle(opts.Title); err != nil {
		return nil, err
	}
	if len(opts.Description) > 5000 {
		return nil, fmt.Errorf("the description is %d bytes; YouTube allows at most 5000", len(opts.Description))
	}
	if strings.ContainsAny(opts.Description, "<>") {
		return nil, errors.New("the description must not contain < or >")
	}
	if err := oneOf("privacy", opts.Privacy, privacyStatuses...); err != nil {
		return nil, err
	}

	status := &youtube.VideoStatus{
		PrivacyStatus:           opts.Privacy,
		SelfDeclaredMadeForKids: opts.MadeForKids,
		ContainsSyntheticMedia:  opts.SyntheticMedia,
		// "Not made for kids" has to be said out loud, or YouTube leaves
		// the question open and asks it again in Studio.
		ForceSendFields: []string{"SelfDeclaredMadeForKids"},
	}
	if opts.PublishAt != "" {
		at, err := parsePublishAt(opts.PublishAt, now)
		if err != nil {
			return nil, err
		}
		status.PublishAt = at.UTC().Format(time.RFC3339)
		status.PrivacyStatus = "private"
	}

	return &youtube.Video{
		Snippet: &youtube.VideoSnippet{
			Title:       opts.Title,
			Description: opts.Description,
			Tags:        opts.Tags,
			CategoryId:  opts.Category,
		},
		Status: status,
	}, nil
}

// parsePublishAt reads a --publish-at value, which must be a full
// timestamp later than now.
func parsePublishAt(value string, now time.Time) (time.Time, error) {
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --publish-at %q: use a full timestamp with a UTC offset, like 2030-01-31T09:00:00+08:00", value)
	}
	if !at.After(now) {
		return time.Time{}, fmt.Errorf("invalid --publish-at %q: that time has already passed", value)
	}
	return at, nil
}

// progress reports an upload's advance on one rewritten line. It only
// draws on a terminal: in a log or a pipe the carriage returns would pile
// up as garbage.
type progress struct {
	w     io.Writer
	total int64
	live  bool
	drawn bool
}

func newProgress(w io.Writer, total int64) *progress {
	p := &progress{w: w, total: total}
	if f, ok := w.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			p.live = info.Mode()&os.ModeCharDevice != 0
		}
	}
	if !p.live && w != io.Discard {
		if total > 0 {
			fmt.Fprintf(w, "Uploading %.1f MB...\n", mb(total))
		} else {
			fmt.Fprintln(w, "Uploading...")
		}
	}
	return p
}

func (p *progress) update(sent int64) {
	if !p.live {
		return
	}
	p.drawn = true
	if p.total <= 0 {
		fmt.Fprintf(p.w, "\rUploading %.1f MB", mb(sent))
		return
	}
	fmt.Fprintf(p.w, "\rUploading %.1f / %.1f MB (%d%%)", mb(sent), mb(p.total), sent*100/p.total)
}

func (p *progress) finish() {
	if p.drawn {
		fmt.Fprintln(p.w)
	}
}

func mb(n int64) float64 { return float64(n) / (1 << 20) }
