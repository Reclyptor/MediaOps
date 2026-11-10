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

Manifest paths stay `MEDIA_ROOT`-relative (`dxp6800/anime/…`), but `/mnt/media`
is a plain directory with one mount per NAS beneath it — **not** a share. Never
synthesize a sibling path from `MEDIA_ROOT`: it lands on the pod's overlay and is
destroyed by the next redeploy. Scratch space resolves the share from the file
with `mkvlib.share_root()`; the archive is a configured tree, not a sibling.

The archive is **one tree, on the box that is not being worked**, set by
`ARCHIVE_ROOT`. It has no default — `mkvlib.archive_root()` refuses to run unless
it is set, exists, sits on a different device than `/`, and is writable. The value
comes from a sops-encrypted Secret, not from the deployment manifest: that repo is
public, and dataset names are not published there.

A file archives to the archive root plus its path *within its share*, so
`<box>/<category>/X/Y.mkv` becomes `<ARCHIVE_ROOT>/<category>/X/Y.mkv`. The box
name is dropped because a category lives on exactly one box. The working
categories and the archive sit on different NAS permanently, so an archive move
is normally a cross-filesystem copy rather than a rename.

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
| `mkv-archive MANIFEST.json` | move superseded library files into `$ARCHIVE_ROOT/`, layout preserved |
| `mkv-promote MANIFEST.json` | move verified files into the library, with accounting |
| `mkv-order INVENTORY.jsonl SERIES` | prove a disc's track order IS episode order, from its Play-All |
| `mkv-anilist "<search>"` | episode counts and titles from AniList |
| `mkv-fingerprint batch\|match` | prove episode adjacency from next-episode previews |
| `mkv-dupcheck DIR TRACK` | is a track an alternate cut of another, or distinct content? |
| `mkv-titlescan FILE OUT START END [STEP] [BAND]` | hunt for an episode title card |
| `mkv-staticcheck FILE...` | is this a real extra, or a disc menu / logo card? |
| `mkv-drives [--labels]` | one JSON line per optical drive: serial, USB port, link speed, tray/media |
| `mkv-rip PLAN.json` / `status ID` / `wait ID` | staggered disc ripping into `_pending`, verified, with a manifest |

Every mutating tool is **dry-run by default** and needs `--go` to act. The exception is
`mkv-rip`, which only ever writes new `.partial` directories and refuses to touch an
existing destination.

Also on `PATH`: `mkvmerge`, `mkvpropedit`, `mkvinfo`, `mkvextract`, `ffmpeg`,
`ffprobe`, `mediainfo`, `montage`, `python3`, `jq`, `rsync`.

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

`mkv-titlescan` exists because a show's title card is often a text overlay on an
ordinary shot rather than a full-screen card, it is on screen for only ~4-5 seconds, and
it does not sit at a fixed offset — the OP moves with the length of the cold open.
Sampling a couple of guessed timestamps will miss it and leave you concluding the show
has no titles.

It defaults to the **whole frame**. An earlier version hardcoded the lower third, which
produced a false negative on a series whose card renders upper-right and nearly did on
another whose card sat just above the crop line — both were then written off as "no title
card", the worst possible failure for an identity check. `BAND` (`0.55`, `0.30:0.70`, …)
narrows the crop to gain resolution, but only *after* you know where that series puts its
card.

**Run it against an episode whose card you have already seen before trusting a negative
result.** An instrument that finds nothing is indistinguishable from one pointed the
wrong way — that mistake has been made here already.

`mkv-staticcheck` guards the other end of the pipeline. MakeMKV rips a disc's menu
backgrounds and logo cards as ordinary titles; they are the same length as a creditless
OP, so filing by runtime alone puts them in `Extras/`. A menu card is a *still* held for
40–90 seconds, so hashing at 1 fps and counting distinct frames separates them cleanly —
0.01–0.04 for furniture against 0.76–0.99 for real content, with no overlap observed.

Check what a disc's short tracks actually *are* before filing them. On one pass this
caught three Funimation trailers for entirely unrelated shows sitting inside a series'
Extras folder.

## Ripping discs

`makemkvcon` is built into the image (a self-contained glibc bundle under
`/opt/makemkv`, since the image itself is musl), and `mkv-drives` / `mkv-rip` drive
the eight BP60NB10 drives on `styxeon`.

**The pod must be privileged** to see `/dev/sr*` and `/dev/sg*`, with `cdrom` (24) in
`supplementalGroups`. It still runs as uid 3000. Unprivileged, `mkv-drives` can still
read serials and link speeds from sysfs, but it cannot read tray or media state and
`mkv-rip` will block every disc.

The drives share one powered USB 2.0 hub tree, and spinning several up at once has
knocked drives off the bus. So `mkv-rip` is a controller, not a loop: one drive scans
at a time, the next waits `spinup_gap_seconds` after the previous one starts copying,
and any USB error pauses new starts. A drive whose link is not 480 Mbit/s is blocked
before it spins up — the fix for that needs root on the node.

```bash
mkv-drives                       # what is in the bays, and at what link speed
mkv-rip plan.json                # prints a run id; the rip survives a dropped exec
mkv-rip status <run-id>          # per-disc state and progress
mkv-rip wait <run-id>            # blocks; exits 0 only if every disc is done
mkv-rip list                     # every run; a new run is refused while one is running
```

Rips land in `$MEDIA_ROOT/dxp6800/_pending/` as `<dest>.partial/` and are renamed into
place only after the run passes: makemkvcon exit 0, no read errors, every file's
duration within 2% of what MakeMKV reported. A `manifest.json` records every title, so
classification can read it instead of re-probing. The plan format and the full rules
are in [`SPEC.md` §10](SPEC.md#10-disc-ripping-mkv-drives-and-mkv-rip).

The licence key arrives as `$MAKEMKV_KEY` and is written to `$HOME/.MakeMKV/` on the
`/work` emptyDir. It never touches the NAS.

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
one session lost its entire working set to a `/tmp` clear.

This matters more than it looks. The deployment is `strategy: Recreate` with Flux image
automation, so **pushing a new image redeploys the pod and kills anything running in it**,
and `/work` is an `emptyDir` that goes with it. Long jobs must write to `/mnt/media`, and
should be resumable — `mkv-fingerprint batch` skips work already on disk for exactly this
reason.
