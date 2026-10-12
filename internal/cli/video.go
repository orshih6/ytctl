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
	"github.com/orshih6/ytctl/internal/output"
	"github.com/orshih6/ytctl/internal/yt"
)

var searchOrders = []string{"relevance", "date", "viewCount", "rating", "title"}

func newVideoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "video",
		Short: "Search, inspect and edit videos",
	}
	cmd.AddCommand(
		newVideoSearchCommand(),
		newVideoListCommand(),
		newVideoGetCommand(),
		newVideoUpdateCommand(),
		newVideoRateCommand(),
		newVideoCategoriesCommand(),
		newVideoThumbnailCommand(),
	)
	return cmd
}

func newVideoSearchCommand() *cobra.Command {
	var (
		limit int64
		order string
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search all of YouTube for videos (100 quota units)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := errors.Join(checkLimit(limit, 50), oneOf("order", order, searchOrders...)); err != nil {
				return err
			}
			return runSearch(cmd, yt.VideoSearch{Query: args[0], Order: order, Limit: limit})
		},
	}
	cmd.Flags().Int64Var(&limit, "limit", 25, "number of results, 1-50")
	cmd.Flags().StringVar(&order, "order", "relevance", "sort order: "+strings.Join(searchOrders, ", "))
	return cmd
}

func newVideoListCommand() *cobra.Command {
	var (
		limit   int64
		order   string
		channel string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your own videos, or another channel's with --channel (100 quota units)",
		Long: "List your own videos, private and unlisted ones included, or another\n" +
			"channel's public videos with --channel. Results carry no view counts; use\n" +
			"`ytctl video get` for those.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := errors.Join(checkLimit(limit, 50), oneOf("order", order, searchOrders...)); err != nil {
				return err
			}
			search := yt.VideoSearch{ChannelID: channel, Mine: channel == "", Order: order, Limit: limit}
			return runSearch(cmd, search)
		},
	}
	cmd.Flags().Int64Var(&limit, "limit", 25, "number of results, 1-50")
	cmd.Flags().StringVar(&order, "order", "date", "sort order: "+strings.Join(searchOrders, ", "))
	cmd.Flags().StringVar(&channel, "channel", "", "list this channel's videos instead of your own")
	return cmd
}

func runSearch(cmd *cobra.Command, search yt.VideoSearch) error {
	client, err := newClient(cmd)
	if err != nil {
		return err
	}
	results, err := client.SearchVideos(cmd.Context(), search)
	if err != nil {
		return err
	}
	return emit(cmd, orEmpty(results), func(w io.Writer) error {
		rows := make([][]string, 0, len(results))
		for _, r := range results {
			row := []string{"", "", "", ""}
			if r.Id != nil {
				row[0] = r.Id.VideoId
			}
			if r.Snippet != nil {
				row[1], row[2], row[3] = date(r.Snippet.PublishedAt), output.Truncate(r.Snippet.ChannelTitle, 24), output.Truncate(r.Snippet.Title, 70)
			}
			rows = append(rows, row)
		}
		return output.Table(w, []string{"ID", "PUBLISHED", "CHANNEL", "TITLE"}, rows)
	})
}

func newVideoGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <video-id>",
		Short: "Show a video's details and statistics",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			v, err := client.Video(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(cmd, v, func(w io.Writer) error { return writeVideo(w, v) })
		},
	}
}

func newVideoUpdateCommand() *cobra.Command {
	var title, description, tags, category, privacy, publishAt string

	cmd := &cobra.Command{
		Use:   "update <video-id>",
		Short: "Change a video's title, description, tags, category, privacy or schedule",
		Long: "Change a video's metadata. Only the flags you pass are changed; everything\n" +
			"else about the video is kept as it is.\n\n" +
			"--publish-at schedules or reschedules a private video that has never been\n" +
			"public; --publish-at \"\" cancels the schedule and leaves it private.",
		Example: "  ytctl video update dQw4w9WgXcQ --title \"Better title\"\n" +
			"  ytctl video update dQw4w9WgXcQ --publish-at 2030-01-31T09:00:00+08:00",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var patch yt.VideoPatch
			flags := cmd.Flags()
			if flags.Changed("title") {
				if err := checkTitle(title); err != nil {
					return err
				}
				patch.Title = &title
			}
			if flags.Changed("description") {
				patch.Description = &description
			}
			if flags.Changed("tags") {
				split := meta.SplitTags(tags)
				if split == nil {
					split = []string{}
				}
				patch.Tags = &split
			}
			if flags.Changed("category") {
				patch.CategoryID = &category
			}
			if flags.Changed("privacy") {
				if err := oneOf("privacy", privacy, privacyStatuses...); err != nil {
					return err
				}
				patch.PrivacyStatus = &privacy
			}
			if flags.Changed("publish-at") {
				if publishAt != "" {
					if patch.PrivacyStatus != nil && privacy != "private" {
						return fmt.Errorf("--publish-at cannot be combined with --privacy %s: a scheduled video is private until its time comes", privacy)
					}
					at, err := parsePublishAt(publishAt, time.Now())
					if err != nil {
						return err
					}
					publishAt = at.UTC().Format(time.RFC3339)
				}
				patch.PublishAt = &publishAt
			}
			if patch == (yt.VideoPatch{}) {
				return errors.New("nothing to update: pass at least one of --title, --description, --tags, --category, --privacy, --publish-at")
			}

			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			v, err := client.UpdateVideo(cmd.Context(), args[0], patch)
			if err != nil {
				return err
			}
			return emit(cmd, v, func(w io.Writer) error { return writeVideo(w, v) })
		},
	}

	cmd.Flags().StringVar(&title, "title", "", "new title, at most 100 characters")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().StringVar(&tags, "tags", "", "new comma-separated tags, replacing the current ones (\"\" clears them)")
	cmd.Flags().StringVar(&category, "category", "", "new category ID (list them with: ytctl video categories)")
	cmd.Flags().StringVar(&privacy, "privacy", "", "new privacy: "+strings.Join(privacyStatuses, ", "))
	cmd.Flags().StringVar(&publishAt, "publish-at", "", "new scheduled publication time, e.g. 2030-01-31T09:00:00+08:00 (\"\" cancels the schedule)")
	return cmd
}

func newVideoRateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rate <video-id> <like|dislike|none>",
		Short: "Like or dislike a video, or remove your rating",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, rating := args[0], args[1]
			switch rating {
			case "like", "dislike", "none":
			default:
				return fmt.Errorf("invalid rating %q: must be one of like, dislike, none", rating)
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			if err := client.RateVideo(cmd.Context(), id, rating); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"videoId": id, "rating": rating}, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Rated %s: %s\n", id, rating)
				return err
			})
		},
	}
}

func newVideoCategoriesCommand() *cobra.Command {
	var region string
	cmd := &cobra.Command{
		Use:   "categories",
		Short: "List the video category IDs available in a region",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			categories, err := client.Categories(cmd.Context(), region)
			if err != nil {
				return err
			}
			return emit(cmd, orEmpty(categories), func(w io.Writer) error {
				rows := make([][]string, 0, len(categories))
				for _, c := range categories {
					assignable := "no"
					if c.Snippet.Assignable {
						assignable = "yes"
					}
					rows = append(rows, []string{c.Id, c.Snippet.Title, assignable})
				}
				return output.Table(w, []string{"ID", "TITLE", "ASSIGNABLE"}, rows)
			})
		},
	}
	cmd.Flags().StringVar(&region, "region", "US", "two-letter country code")
	return cmd
}

func newVideoThumbnailCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "thumbnail <video-id> <image>",
		Short: "Set a video's custom thumbnail from a JPEG or PNG (at most 2 MB)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[1])
			if err != nil {
				return err
			}
			defer f.Close()

			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			if err := client.SetThumbnail(cmd.Context(), args[0], f); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"videoId": args[0], "thumbnail": args[1]}, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Thumbnail set for %s\n", args[0])
				return err
			})
		},
	}
}

// checkTitle enforces YouTube's limits on a title before a request (or a
// whole upload) is spent finding them out.
func checkTitle(title string) error {
	switch {
	case strings.TrimSpace(title) == "":
		return errors.New("the title must not be empty")
	case len([]rune(title)) > 100:
		return fmt.Errorf("the title is %d characters; YouTube allows at most 100", len([]rune(title)))
	case strings.ContainsAny(title, "<>"):
		return errors.New("the title must not contain < or >")
	}
	return nil
}

func writeVideo(w io.Writer, v *youtube.Video) error {
	fields := []output.Field{
		{Name: "ID", Value: v.Id},
		{Name: "URL", Value: "https://youtu.be/" + v.Id},
	}
	if s := v.Snippet; s != nil {
		fields = append(fields,
			output.Field{Name: "Title", Value: s.Title},
			output.Field{Name: "Channel", Value: s.ChannelTitle},
			output.Field{Name: "Published", Value: date(s.PublishedAt)},
			output.Field{Name: "Category", Value: s.CategoryId},
			output.Field{Name: "Tags", Value: strings.Join(s.Tags, ", ")},
		)
	}
	if st := v.Status; st != nil {
		fields = append(fields,
			output.Field{Name: "Privacy", Value: st.PrivacyStatus},
			output.Field{Name: "Scheduled for", Value: st.PublishAt},
			output.Field{Name: "Upload status", Value: st.UploadStatus},
		)
	}
	if cd := v.ContentDetails; cd != nil {
		fields = append(fields, output.Field{Name: "Duration", Value: strings.ToLower(strings.TrimPrefix(cd.Duration, "PT"))})
	}
	if st := v.Statistics; st != nil {
		fields = append(fields,
			output.Field{Name: "Views", Value: count(st.ViewCount)},
			output.Field{Name: "Likes", Value: count(st.LikeCount)},
			output.Field{Name: "Comments", Value: count(st.CommentCount)},
		)
	}
	if s := v.Snippet; s != nil {
		fields = append(fields, output.Field{Name: "Description", Value: s.Description})
	}
	return output.Fields(w, fields)
}
