package cli

import (
	"errors"
	"fmt"
	"io"
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
		Use:   "upload <video-file>",
		Short: "Upload a video (1,600 quota units)",
		Long: "Upload a video file. It is uploaded as private unless --privacy says\n" +
			"otherwise.\n\n" +
			"--meta reads the title, description and tags from a Markdown file with\n" +
			"\"## Title\", \"## Description\" and \"## Tags\" sections; a flag given as\n" +
			"well overrides that field.\n\n" +
			"--publish-at schedules the video: YouTube only schedules private videos,\n" +
			"so it is uploaded as private and goes public at that time.",
		Example: "  ytctl upload out/video.mp4 --meta youtube.md\n" +
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
			file, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				return err
			}
			if info.IsDir() || info.Size() == 0 {
				return fmt.Errorf("%s is not a video file", args[0])
			}
			var thumb *os.File
			if thumbnail != "" {
				if thumb, err = os.Open(thumbnail); err != nil {
					return err
				}
				defer thumb.Close()
			}

			client, err := newClient(cmd)
			if err != nil {
				return err
			}

			progress := newProgress(stderr, info.Size())
			uploaded, err := client.Upload(cmd.Context(), video, notify, file, progress.update)
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
				fmt.Fprintf(cmd.ErrOrStderr(), "Note: asked for %s but YouTube made the video %s. Uploads from a Google Cloud project that has not passed YouTube's API audit are locked to private.\n", video.Status.PrivacyStatus, result.PrivacyStatus)
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
	f.BoolVar(&notify, "notify-subscribers", false, "notify the channel's subscribers about the upload")
	f.StringVar(&thumbnail, "thumbnail", "", "JPEG or PNG to set as the thumbnail after the upload")
	f.StringVar(&playlist, "playlist", "", "playlist ID to add the video to after the upload")
	return cmd
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
		// "Not made for kids" has to be said out loud, or YouTube leaves
		// the question open and asks it again in Studio.
		ForceSendFields: []string{"SelfDeclaredMadeForKids"},
	}
	if opts.PublishAt != "" {
		at, err := time.Parse(time.RFC3339, opts.PublishAt)
		if err != nil {
			return nil, fmt.Errorf("invalid --publish-at %q: use a full timestamp with a UTC offset, like 2030-01-31T09:00:00+08:00", opts.PublishAt)
		}
		if !at.After(now) {
			return nil, fmt.Errorf("invalid --publish-at %q: that time has already passed", opts.PublishAt)
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
		fmt.Fprintf(w, "Uploading %.1f MB...\n", mb(total))
	}
	return p
}

func (p *progress) update(sent int64) {
	if !p.live {
		return
	}
	p.drawn = true
	fmt.Fprintf(p.w, "\rUploading %.1f / %.1f MB (%d%%)", mb(sent), mb(p.total), sent*100/p.total)
}

func (p *progress) finish() {
	if p.drawn {
		fmt.Fprintln(p.w)
	}
}

func mb(n int64) float64 { return float64(n) / (1 << 20) }
