# mediaops

A media-library processing workbench that runs **on the cluster, wired to the
NAS** — not on the workstation, which reaches the NAS only over WiFi at
~12.5 MB/s.

Everything here is a stream copy or a probe. Nothing re-encodes.

See [`SPEC.md`](SPEC.md) for the full design, the constraints it operates under,
and — importantly — [what it does *not* solve](SPEC.md#6-what-this-does-not-solve).

## Deployment

Deployed by Flux from [`Reclyptor/Kubernetes`](https://github.com/Reclyptor/Kubernetes)
as `apps/mediaops`. It is a single long-lived pod pinned to **`styxeon`**, the
worker with the wired 10G path to the NAS, with `/mnt/primary/videos` mounted
read-write at `/mnt/media`.

```bash
kubectl -n mediaops exec -it deploy/mediaops -- bash
```

The pod runs as **uid 3000, gid 100**, matching every file already on the share.
No process in it is ever root, so a root-owned media file is not merely avoided
by convention — it is unrepresentable.

## Tools

| Command | Purpose |
|---|---|
| `mkv-probe DIR` | inventory a tree as JSON lines: duration, chapters, streams |
| `mkv-bounds FILE EP_MINUTES` | find episode boundaries in a Play-All track |
| `mkv-frames FILE OUT.png T...` | timestamped frames tiled into one contact sheet |
| `mkv-split FILE PREFIX CH,CH` | split on chapter numbers, then verify the result |
| `mkv-archive MANIFEST.json` | move superseded library files into `_archive/` |
| `mkv-promote MANIFEST.json` | move verified files into the library, with accounting |
| `mkv-order INVENTORY.jsonl SERIES` | prove a disc's track order IS episode order, from its Play-All |
| `mkv-anilist "<search>"` | episode counts and titles from AniList |
| `mkv-fingerprint batch\|match` | prove episode adjacency from next-episode previews |
| `mkv-dupcheck DIR TRACK` | is a track an alternate cut of another, or distinct content? |

Every mutating tool is **dry-run by default** and needs `--go` to act.

Also on `PATH`: `mkvmerge`, `mkvpropedit`, `mkvinfo`, `mkvextract`, `ffmpeg`,
`ffprobe`, `mediainfo`, `python3`, `jq`, `rsync`.

`mkv-order` is the cheapest verification here and worth reaching for first: lay a
disc's episode files end to end and check whether their chapter marks reproduce the
Play-All's. If they do, track order is episode order — proven from the disc's own
authoring, no frames needed. It still cannot tell you *which* episode the first track
is; that always needs content.

When there is no Play-All — and no title card, and the subtitles are dialogue-only —
`mkv-fingerprint` is the fallback that still works. Every episode's closing preview is
built from clips of the *next* episode, so those frames literally occur in the successor.
Reduce every frame to a 64-bit difference hash, then ask, all-pairs, which track's body
contains this track's preview. On The Qwaser of Stigmata all 34 links came back with
match 0.61–0.99 against runners-up of 0.07–0.39 — and it proved that one track was
skipped by the chain entirely, which is how the surplus OVA was found.

`mkv-dupcheck` answers the follow-up: a censored/uncensored pair or a director's cut
shares most of its frames with its twin, whereas two different episodes of the same show
share only their OP and ED — about 0.13 in practice.

## The one thing to remember

`mkv-bounds` **cannot tell you a cut lands in the right place.** A set of cuts
shifted by a constant offset has the same stride, the same spread, the same fit
error and the same episode length as the correct set. No statistic separates
them — this was confirmed on a track that graded `auto` at fit error 0.226 and
cut 1.57 minutes *inside* every episode.

Only frames can settle it. Run `mkv-frames` on one interior boundary per track
before splitting. Of the tracks that were actually checked, roughly a third were
anchored wrong.

The structure to read for:

```
ED credits → (post-ED bumper) → next-episode preview → CUT → cold open → OP → title card
```

The 次回予告 next-episode preview belongs to the **outgoing** episode. Anchoring
on its title card puts every cut one chapter late.

## Working state

Tools live in the image; **working state lives on the NAS** under
`_pending/anime/_plans/` (`bin/`, `state/`). Iterating on a plan therefore never
requires an image rebuild, and nothing important is ever kept in a scratchpad —
the previous session lost its entire working set to a `/tmp` clear.
