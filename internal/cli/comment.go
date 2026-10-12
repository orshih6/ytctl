package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"google.golang.org/api/youtube/v3"
)

func newCommentCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "comment",
		Short: "Read and write comments",
	}
	cmd.AddCommand(newCommentListCommand(), newCommentPostCommand(), newCommentReplyCommand())
	return cmd
}

func newCommentListCommand() *cobra.Command {
	var (
		limit int64
		order string
	)
	cmd := &cobra.Command{
		Use:   "list <video-id>",
		Short: "List a video's comments and their replies",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := errors.Join(checkLimit(limit, 100), oneOf("order", order, "time", "relevance")); err != nil {
				return err
			}
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			threads, err := client.CommentThreads(cmd.Context(), args[0], order, limit)
			if err != nil {
				return err
			}
			return emit(cmd, orEmpty(threads), func(w io.Writer) error {
				for _, t := range threads {
					if t.Snippet == nil || t.Snippet.TopLevelComment == nil {
						continue
					}
					writeComment(w, "", t.Id, t.Snippet.TopLevelComment)
					if t.Replies != nil {
						for _, r := range t.Replies.Comments {
							writeComment(w, "    ", "", r)
						}
					}
					fmt.Fprintln(w)
				}
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&limit, "limit", 20, "number of comment threads, 1-100")
	cmd.Flags().StringVar(&order, "order", "time", "sort order: time, relevance")
	return cmd
}

func newCommentPostCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "post <video-id> <text>",
		Short: "Post a public comment on a video",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			thread, err := client.PostComment(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			return emit(cmd, thread, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Posted comment thread %s\n", thread.Id)
				return err
			})
		},
	}
}

func newCommentReplyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "reply <thread-id> <text>",
		Short: "Reply publicly to a comment thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newClient(cmd)
			if err != nil {
				return err
			}
			reply, err := client.ReplyToComment(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			return emit(cmd, reply, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Posted reply %s\n", reply.Id)
				return err
			})
		},
	}
}

// writeComment prints one comment. Top-level comments lead with the thread
// ID, which is what `comment reply` takes.
func writeComment(w io.Writer, indent, threadID string, c *youtube.Comment) {
	s := c.Snippet
	if s == nil {
		return
	}
	header := fmt.Sprintf("%s  %s", s.AuthorDisplayName, date(s.PublishedAt))
	if s.LikeCount > 0 {
		header += fmt.Sprintf("  %d likes", s.LikeCount)
	}
	if threadID != "" {
		header = threadID + "  " + header
	}
	text := s.TextDisplay
	if text == "" {
		text = s.TextOriginal
	}
	fmt.Fprintf(w, "%s%s\n%s%s\n", indent, header, indent, text)
}
