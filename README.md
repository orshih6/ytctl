# ytctl

A command-line client for the YouTube Data API: upload videos, edit their metadata, and
manage playlists and comments from a shell or a script.

```console
$ ytctl upload out/kettle.mp4 --meta youtube.md --thumbnail thumb.png
Uploaded dQw4w9WgXcQ
https://youtu.be/dQw4w9WgXcQ
Privacy: private
```

> **Status: pre-release.** Everything except `upload` has been run against the real API.
> Uploading is covered by automated tests but has not yet completed a real upload, so
> there is no tagged release yet. Install from source for now.

It signs in as you, with an OAuth client you create in your own Google Cloud project. There
is no server in between and nothing is sent anywhere except to Google.

## Install

```bash
go install github.com/orshih6/ytctl/cmd/ytctl@latest
```

Binaries for macOS, Linux and Windows will be on the
[releases page](https://github.com/orshih6/ytctl/releases) from the first tagged release.

## Setup

ytctl needs an OAuth client of its own. This is a one-time, five-minute job:

1. In the [Google Cloud console](https://console.cloud.google.com/), create a project (or
   pick one) and enable **YouTube Data API v3** under *APIs & Services → Library*.
2. Under *APIs & Services → OAuth consent screen*, configure the consent screen. While the
   app is in "Testing", add your own Google account as a test user.
3. Under *APIs & Services → Credentials*, create an **OAuth client ID** of type
   **Desktop app** and download its JSON.
4. Tell ytctl about it, in any one of these ways:

   ```bash
   # a) point at the downloaded file
   ytctl --client-secret-file ~/Downloads/client_secret_123.json auth login

   # b) environment
   export YTCTL_CLIENT_ID=...apps.googleusercontent.com
   export YTCTL_CLIENT_SECRET=...

   # c) ~/.config/ytctl/config.yaml
   client_secret_file: /path/to/client_secret.json
   # or
   client_id: ...apps.googleusercontent.com
   client_secret: ...
   ```

   Precedence is flag, then environment, then config file.

5. Sign in:

   ```bash
   ytctl auth login      # opens a browser; --no-browser just prints the URL
   ytctl auth status
   ```

The token is stored in `~/.config/ytctl/token.json`, readable only by you, and is refreshed
automatically. `ytctl auth logout` revokes it and deletes the file.

> **Tokens that expire after a week.** While the consent screen is in "Testing", Google
> expires refresh tokens after 7 days and you have to log in again. Publishing the app
> (*OAuth consent screen → Publish app*) lifts that; no verification is needed if you are
> the only user and accept the "unverified app" warning.

## Commands

| Command | What it does |
|---|---|
| `ytctl upload <file>` | Upload a video |
| `ytctl video list [--channel ID]` | List your videos, or a channel's, with privacy, schedule and views |
| `ytctl video search <query>` | Search YouTube |
| `ytctl video get <id>` | Details and statistics |
| `ytctl video update <id>` | Change title, description, tags, category, privacy, schedule |
| `ytctl video rate <id> like\|dislike\|none` | Rate a video |
| `ytctl video thumbnail <id> <image>` | Set a custom thumbnail |
| `ytctl video categories [--region US]` | List category IDs |
| `ytctl channel mine` / `get <id>` / `find <@handle>` | Look up channels |
| `ytctl playlist list` / `get` / `create` / `update` / `delete` | Manage playlists |
| `ytctl playlist items <id>` / `add <id> <video>` / `remove <item-id>` | Manage playlist contents |
| `ytctl comment list <video>` / `post <video> <text>` / `reply <thread> <text>` | Comments |
| `ytctl auth login` / `status` / `logout` | Sign in and out |

Every command has `--help`. Global flags:

- `--json` prints the API's own response instead of a table, for scripts and agents.
- `--quiet` suppresses the upload progress line.
- `--yes` skips the confirmation before `playlist delete` and `playlist remove`.

### Uploading

```bash
ytctl upload clip.mp4 \
  --title "How a kettle knows when to stop #Shorts" \
  --description "It is a bimetallic disc." \
  --tags "kettle, science" \
  --privacy unlisted \
  --thumbnail thumb.png \
  --playlist PLxxxxxxxx
```

- Videos are uploaded as **private** unless `--privacy` says otherwise.
- `--publish-at 2030-01-31T09:00:00+08:00` schedules the video. YouTube only schedules
  private videos, so it is uploaded private and goes public at that time.
- `--category` defaults to `28` (Science & Technology); see `ytctl video categories`.
- Subscribers are not notified unless you pass `--notify-subscribers`.
- The video is declared *not* made for kids unless you pass `--made-for-kids`.
- `--synthetic-media` discloses realistic altered or AI-generated content, as YouTube
  requires for such videos.
- Uploads are resumable and sent in 8 MB chunks, so a dropped connection costs one chunk,
  not the whole file.
- A vertical video of three minutes or less becomes a Short on its own; there is no flag
  for it.

If the upload succeeds but the thumbnail or playlist step fails, ytctl still prints the new
video's ID and exits non-zero, so nothing is uploaded twice by a retry.

### Metadata files

`--meta` reads the title, description and tags from a Markdown file, which is convenient to
keep next to the video's source:

```markdown
# Notes for myself (ignored)

## Title
How a kettle knows when to stop #Shorts

## Description
Ever wondered why it clicks off?

🔥 Water boils
💨 Steam reaches a bimetallic disc

#shorts #science

## Tags
kettle, how it works, science
```

```bash
ytctl upload out/kettle.mp4 --meta youtube.md
ytctl upload out/kettle.mp4 --meta youtube.md --title "A/B test title"   # flags win
```

Headings are case-insensitive, sections may come in any order, and sections ytctl does not
know are ignored.

### Editing

`video update` and `playlist update` change **only the flags you pass**:

```bash
ytctl video update dQw4w9WgXcQ --title "Better title"     # description and tags untouched
ytctl video update dQw4w9WgXcQ --tags ""                  # clears the tags
ytctl video update dQw4w9WgXcQ --privacy public
ytctl video update dQw4w9WgXcQ --publish-at 2030-02-01T09:00:00+08:00   # reschedule
ytctl video update dQw4w9WgXcQ --publish-at ""                          # cancel the schedule
```

`--publish-at` works on a private video that has never been public. ytctl refuses to
schedule a video that is currently public or unlisted unless you also pass
`--privacy private`, because that takes it offline until the scheduled time.

(The API itself replaces everything you leave out; ytctl reads the video first and sends
the current values back.)

### Scripting

```bash
# IDs of my 10 newest videos
ytctl video list --limit 10 --json | jq -r '.[].id'

# what is scheduled, and for when
ytctl video list --json | jq -r '.[] | select(.status.publishAt) | "\(.status.publishAt)  \(.snippet.title)"'

# upload and capture the ID
id=$(ytctl upload clip.mp4 --meta youtube.md --json -q | jq -r .videoId)
```

Results go to stdout; progress, prompts and errors go to stderr. The exit status is 0 on
success, 1 on failure and 130 when interrupted.

## Things YouTube decides

- **Quota.** By default a Google Cloud project gets 100 uploads a day and 100 searches a
  day, each counted on its own, and 10,000 units a day for everything else (reads cost 1,
  writes cost 50). All of it resets at midnight Pacific time. `video search` uses a search;
  `video list` does not, unless you pass an `--order` other than `date`.
- **Privacy can be overridden.** YouTube may give an upload a different privacy than you
  asked for, for example from the channel's upload defaults. ytctl tells you when that
  happens and how to change it.
- **Custom thumbnails** need a channel that is allowed to use them (YouTube asks for phone
  verification), and the image must be a JPEG or PNG of at most 50 MB.
- `channel find` with a plain name looks up a *legacy username*, which most channels do not
  have. Use the `@handle`.

## Not supported

Deleting videos, captions, analytics and live streams.

## Development

```bash
make check     # gofmt, go vet, tests with the race detector
make build     # bin/ytctl
```

The tests run against a local fake of the API; none of them contact Google.

## License

[MIT](LICENSE)
