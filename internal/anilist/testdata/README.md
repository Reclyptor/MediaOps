These responses were recorded from graphql.anilist.co on 2026-11-14 with the
query in anilist.go.

- search-penguindrum.json: a search for Penguindrum (HTTP 200).
- id-21355.json: `--id 21355` (HTTP 200). Its streamingEpisodes are out of
  broadcast order, which is why the output carries an advisory note.
- notfound.json: an id that does not exist (HTTP 404 with an `errors` body).
