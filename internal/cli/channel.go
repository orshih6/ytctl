package cli

import (
	"io"
	"strconv"

	"github.com/spf13/cobra"
	"google.golang.org/api/youtube/v3"

	"github.com/orshih6/ytctl/internal/output"
)

func newChannelCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "Look up channels",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "mine",
			Short: "Show the signed-in account's channel",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				client, err := newClient(cmd)
				if err != nil {
					return err
				}
				ch, err := client.MyChannel(cmd.Context())
				if err != nil {
					return err
				}
				return emit(cmd, ch, func(w io.Writer) error { return writeChannel(w, ch) })
			},
		},
		&cobra.Command{
			Use:   "get <channel-id>",
			Short: "Show a channel by ID",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				client, err := newClient(cmd)
				if err != nil {
					return err
				}
				ch, err := client.Channel(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				return emit(cmd, ch, func(w io.Writer) error { return writeChannel(w, ch) })
			},
		},
		&cobra.Command{
			Use:   "find <@handle | username>",
			Short: "Find a channel by @handle or legacy username",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				client, err := newClient(cmd)
				if err != nil {
					return err
				}
				channels, err := client.FindChannels(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				return emit(cmd, orEmpty(channels), func(w io.Writer) error {
					rows := make([][]string, 0, len(channels))
					for _, ch := range channels {
						row := []string{ch.Id, "", "", ""}
						if ch.Snippet != nil {
							row[1] = ch.Snippet.Title
						}
						if ch.Statistics != nil {
							row[2], row[3] = count(ch.Statistics.SubscriberCount), count(ch.Statistics.VideoCount)
						}
						rows = append(rows, row)
					}
					return output.Table(w, []string{"ID", "TITLE", "SUBSCRIBERS", "VIDEOS"}, rows)
				})
			},
		},
	)
	return cmd
}

func writeChannel(w io.Writer, ch *youtube.Channel) error {
	fields := []output.Field{{Name: "ID", Value: ch.Id}}
	if s := ch.Snippet; s != nil {
		fields = append(fields,
			output.Field{Name: "Title", Value: s.Title},
			output.Field{Name: "Handle", Value: s.CustomUrl},
			output.Field{Name: "Created", Value: date(s.PublishedAt)},
			output.Field{Name: "Country", Value: s.Country},
		)
	}
	if st := ch.Statistics; st != nil {
		subscribers := count(st.SubscriberCount)
		if st.HiddenSubscriberCount {
			subscribers = "hidden"
		}
		fields = append(fields,
			output.Field{Name: "Subscribers", Value: subscribers},
			output.Field{Name: "Videos", Value: count(st.VideoCount)},
			output.Field{Name: "Views", Value: count(st.ViewCount)},
		)
	}
	if cd := ch.ContentDetails; cd != nil && cd.RelatedPlaylists != nil {
		fields = append(fields, output.Field{Name: "Uploads playlist", Value: cd.RelatedPlaylists.Uploads})
	}
	if s := ch.Snippet; s != nil {
		fields = append(fields, output.Field{Name: "Description", Value: s.Description})
	}
	return output.Fields(w, fields)
}

func count(n uint64) string { return strconv.FormatUint(n, 10) }

// date trims an RFC 3339 timestamp to its day.
func date(ts string) string {
	if len(ts) >= 10 {
		return ts[:10]
	}
	return ts
}

// orEmpty makes an empty result print as [] rather than null under --json.
func orEmpty[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
