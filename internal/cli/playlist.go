package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/api/youtube/v3"

	"github.com/orshih6/ytctl/internal/output"
	"github.com/orshih6/ytctl/internal/yt"
)

func newPlaylistCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "playlist",
		Short: "Manage playlists and what is in them",
	}
	cmd.AddCommand(
		newPlaylistListCommand(),
		newPlaylistGetCommand(),
		newPlaylistCreateCommand(),
		newPlaylistUpdateCommand(),
		newPlaylistDeleteCommand(),
		newPlaylistItemsCommand(),
		newPlaylistAddCommand(),
		newPlaylistRemoveCommand(),
	)
	return cmd
}

func newPlaylistListCommand() *cobra.Command {
	var (
		limit   int64
		channel string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your own playlists, or another channel's with --channel",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkLimit(limit, 50); err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			playlists, err := client.Playlists(cmd.Context(), channel, limit)
			if err != nil {
				return err
			}
			return emit(cmd, orEmpty(playlists), func(w io.Writer) error {
				rows := make([][]string, 0, len(playlists))
				for _, p := range playlists {
					row := []string{p.Id, "", "", ""}
					if p.ContentDetails != nil {
						row[1] = strconv.FormatInt(p.ContentDetails.ItemCount, 10)
					}
					if p.Status != nil {
						row[2] = p.Status.PrivacyStatus
					}
					if p.Snippet != nil {
						row[3] = output.Truncate(p.Snippet.Title, 70)
					}
					rows = append(rows, row)
				}
				return output.Table(w, []string{"ID", "VIDEOS", "PRIVACY", "TITLE"}, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&limit, "limit", 25, "number of results, 1-50")
	cmd.Flags().StringVar(&channel, "channel", "", "list this channel's playlists instead of your own")
	return cmd
}

func newPlaylistGetCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "get <playlist-id>",
		Short: "Show a playlist",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			p, err := client.Playlist(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return emit(cmd, p, func(w io.Writer) error { return writePlaylist(w, p) })
		},
	}
}

func newPlaylistCreateCommand() *cobra.Command {
	var description, privacy string
	cmd := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a playlist",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := oneOf("privacy", privacy, privacyStatuses...); err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			p, err := client.CreatePlaylist(cmd.Context(), args[0], description, privacy)
			if err != nil {
				return err
			}
			return emit(cmd, p, func(w io.Writer) error { return writePlaylist(w, p) })
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "playlist description")
	cmd.Flags().StringVar(&privacy, "privacy", "private", "privacy: "+strings.Join(privacyStatuses, ", "))
	return cmd
}

func newPlaylistUpdateCommand() *cobra.Command {
	var title, description, privacy string
	cmd := &cobra.Command{
		Use:   "update <playlist-id>",
		Short: "Change a playlist's title, description or privacy",
		Long: "Change a playlist's metadata. Only the flags you pass are changed;\n" +
			"everything else about the playlist is kept as it is.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var patch yt.PlaylistPatch
			flags := cmd.Flags()
			if flags.Changed("title") {
				if strings.TrimSpace(title) == "" {
					return errors.New("the title must not be empty")
				}
				patch.Title = &title
			}
			if flags.Changed("description") {
				patch.Description = &description
			}
			if flags.Changed("privacy") {
				if err := oneOf("privacy", privacy, privacyStatuses...); err != nil {
					return err
				}
				patch.PrivacyStatus = &privacy
			}
			if patch == (yt.PlaylistPatch{}) {
				return errors.New("nothing to update: pass at least one of --title, --description, --privacy")
			}

			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			p, err := client.UpdatePlaylist(cmd.Context(), args[0], patch)
			if err != nil {
				return err
			}
			return emit(cmd, p, func(w io.Writer) error { return writePlaylist(w, p) })
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().StringVar(&privacy, "privacy", "", "new privacy: "+strings.Join(privacyStatuses, ", "))
	return cmd
}

func newPlaylistDeleteCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <playlist-id>",
		Short: "Delete a playlist (its videos are not deleted)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			if !flagYes {
				// Asking by title, not by an opaque ID, is what gives the
				// question a chance of catching a wrong paste.
				p, err := client.Playlist(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if err := confirm(cmd, fmt.Sprintf("Delete playlist %q? This cannot be undone.", p.Snippet.Title)); err != nil {
					return err
				}
			}
			if err := client.DeletePlaylist(cmd.Context(), args[0]); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"deleted": args[0]}, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Deleted playlist %s\n", args[0])
				return err
			})
		},
	}
}

func newPlaylistItemsCommand() *cobra.Command {
	var limit int64
	cmd := &cobra.Command{
		Use:   "items <playlist-id>",
		Short: "List the videos in a playlist",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkLimit(limit, 50); err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			items, err := client.PlaylistItems(cmd.Context(), args[0], limit)
			if err != nil {
				return err
			}
			return emit(cmd, orEmpty(items), func(w io.Writer) error {
				rows := make([][]string, 0, len(items))
				for _, it := range items {
					row := []string{it.Id, "", ""}
					if it.ContentDetails != nil {
						row[1] = it.ContentDetails.VideoId
					}
					if it.Snippet != nil {
						row[2] = output.Truncate(it.Snippet.Title, 70)
					}
					rows = append(rows, row)
				}
				return output.Table(w, []string{"ITEM ID", "VIDEO ID", "TITLE"}, rows)
			})
		},
	}
	cmd.Flags().Int64Var(&limit, "limit", 25, "number of results, 1-50")
	return cmd
}

func newPlaylistAddCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "add <playlist-id> <video-id>",
		Short: "Add a video to a playlist",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			item, err := client.AddToPlaylist(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			return emit(cmd, item, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Added %s to %s (item %s)\n", args[1], args[0], item.Id)
				return err
			})
		},
	}
}

func newPlaylistRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <item-id>",
		Short: "Remove an entry from a playlist",
		Long: "Remove an entry from a playlist. This takes the ITEM ID shown by\n" +
			"`ytctl playlist items`, not the video ID: the same video can be in a\n" +
			"playlist more than once.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirm(cmd, fmt.Sprintf("Remove item %s from its playlist?", args[0])); err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			if err := client.RemoveFromPlaylist(cmd.Context(), args[0]); err != nil {
				return err
			}
			return emit(cmd, map[string]string{"removed": args[0]}, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Removed item %s\n", args[0])
				return err
			})
		},
	}
}

func writePlaylist(w io.Writer, p *youtube.Playlist) error {
	fields := []output.Field{
		{Name: "ID", Value: p.Id},
		{Name: "URL", Value: "https://www.youtube.com/playlist?list=" + p.Id},
	}
	if s := p.Snippet; s != nil {
		fields = append(fields,
			output.Field{Name: "Title", Value: s.Title},
			output.Field{Name: "Channel", Value: s.ChannelTitle},
			output.Field{Name: "Created", Value: date(s.PublishedAt)},
		)
	}
	if st := p.Status; st != nil {
		fields = append(fields, output.Field{Name: "Privacy", Value: st.PrivacyStatus})
	}
	if cd := p.ContentDetails; cd != nil {
		fields = append(fields, output.Field{Name: "Videos", Value: strconv.FormatInt(cd.ItemCount, 10)})
	}
	if s := p.Snippet; s != nil {
		fields = append(fields, output.Field{Name: "Description", Value: s.Description})
	}
	return output.Fields(w, fields)
}
