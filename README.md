# mediaops

A media-library processing workbench that runs **on the cluster, wired to the
NAS** — not on the workstation, which reaches the NAS only over WiFi at
~12.5 MB/s.

Everything here is a stream copy or a probe. Nothing re-encodes.

See [`SPEC.md`](SPEC.md) for the full design, the constraints it operates under,
and — importantly — [what it does *not* solve](SPEC.md#6-what-this-does-not-solve).

## Deployment

Deployed by Flux from [`Reclyptor/Kubernetes`](https://github.com/Reclyptor/Kubernetes)
as `apps/mediaops`. It is two long-lived pods from this one image: `deploy/mediaops`,
the library workbench (any node, each NAS mounted read-write beneath
`/mnt/media`), and `deploy/mediaops-rip`, the ripper (**`styxeon`**, the worker with
the wired 10G path to the NAS and the optical drives, with only the staging tree
mounted). The disc tools run only in the ripper.

```bash
kubectl -n mediaops exec -it deploy/mediaops -- bash
```

Manifest paths stay `MEDIA_ROOT`-relative (`<box>/<category>/…`), but `/mnt/media`
is a plain directory with one mount per NAS beneath it — **not** a share. Never
synthesize a sibling path from `MEDIA_ROOT`: it lands on the pod's overlay and is
destroyed by the next redeploy. Anything that has to know the share resolves it from the file
(`paths.Config.ShareRoot`); the archive is a configured tree, not a sibling.

The archive is **one tree, on the box that is not being worked**, set by
`ARCHIVE_ROOT`. It has no default — `paths.Config.ArchiveRoot` refuses to run unless
it is set, exists, sits on a different device than `/`, and is writable. The value
comes from a sops-encrypted Secret, not from the deployment manifest: that repo is
public, and dataset names are not published there.

`STAGING_ROOT` is the tree rips land in and the run state lives under. It has no
default either: `paths.Config.Staging` returns an error while it is unset, and
`mkv-rip`, `mkv-promote`, `mkv-archive` and the manifest check refuse to run. The
`mkv-audit` subcommands `films`, `blindspot` and `sweep` take their `ROOT` arguments
from the caller and refuse to run without one.

A file archives to the archive root plus its path *within its share*, so
`<box>/<category>/X/Y.mkv` becomes `<ARCHIVE_ROOT>/<category>/X/Y.mkv`. The box
name is dropped because a category lives on exactly one box. The working
categories and the archive sit on different NAS permanently, so an archive move
is normally a cross-filesystem copy rather than a rename.

The pod runs as **uid 3000, gid 100**. The NAS export squashes every write to
3000:3000, so files on the share are `media:3000` whatever group the process has.
No process in it is ever root, so a root-owned media file is not merely avoided
by convention — it is unrepresentable.

## Tools

| Command | Purpose |
|---|---|
| `mkv-probe PATH... [--workers N]` | inventory files and trees as one JSON object: duration, chapters, streams, per file |
| `mkv-bounds FILE EP_MINUTES` | find episode boundaries in a Play-All track; prints the `chapters:N,M` to split on, with its grade |
| `mkv-frames FILE OUT.png T...` | timestamped frames tiled into one labelled contact sheet |
| `mkv-split SRC OUTDIR/PREFIX CH,CH [--go]` | plan a split on chapter numbers against the source's keyframes, then (`--go`) split and verify it |
| `mkv-split SRC OUTDIR/PREFIX --at T,T [--go]` | the same, on times in seconds |
| `mkv-split verify SRC PART PART...` | prove an existing split: every packet accounted for, the seams, the bytes; reads only |
| `mkv-archive MANIFEST.json [--go]` | move superseded library files into `$ARCHIVE_ROOT/`, layout preserved |
| `mkv-promote MANIFEST.json [--go]` | move verified files into the library, with accounting |
| `mkv-manifest check\|exec MANIFEST [--go]` | replay a manifest of ops against the library, then perform it: renames, archive moves, remux, titles, track flags |
| `mkv-manifest gen rename-series\|archive-tree …` | print a manifest for a series rename or an archive-tree move |
| `mkv-order INVENTORY.json [DIR]` | prove a disc's track order IS episode order, from its Play-All |
| `mkv-anilist SEARCH \| --id N` | episode counts and titles from AniList |
| `mkv-audit series\|films\|blindspot\|sweep\|postcheck …` | read-only audits of the library against the normalisation rules, and of a manifest run against its manifest |
| `mkv-fingerprint hash\|batch\|match` | prove episode adjacency from next-episode previews |
| `mkv-dupcheck DIR TRACK` | is a track an alternate cut of another, or distinct content? |
| `mkv-titlescan FILE OUT.png START END [--step S] [--band B]` | hunt for an episode title card |
| `mkv-staticcheck FILE...` | is this a real extra, or a disc menu / logo card? |
| `mkv-cards find\|freeze FILE...` | every held title-card candidate in a window, by colour or by freeze |
| `mkv-cards sheet OUT.png --candidates\|--points\|--chapters\|--seams …` | one labelled tile per candidate, point, chapter or seam frame |
| `mkv-drives` | a JSON array of the optical drives: serial, USB port, link speed, tray/media |
| `mkv-rip start\|identify\|status\|wait\|list\|finalize\|stop` | staggered disc ripping into the staging tree, verified, with a manifest; `identify` reads the discs' title tables; `finalize` re-verifies failed discs |

Every mutating tool is **dry-run by default** and needs `--go` to act; `mkv-archive` and
`mkv-promote` print one JSON report either way ([`SPEC.md` §11](SPEC.md#11-library-moves-mkv-archive-and-mkv-promote)), as does
`mkv-manifest exec`, which runs `check` first on every invocation, dry or not, and refuses on
any finding ([`SPEC.md` §12](SPEC.md#12-manifest-runs-mkv-manifest)). The exception is
`mkv-rip`, which only ever writes new `.partial` directories and refuses to touch an
existing destination. `mkv-split` follows the same rule with `.partial` files: it plans
and checks without writing a byte until `--go`, then places the parts without overwriting
and never touches the source ([`SPEC.md` §13](SPEC.md#13-play-all-analysis-splitting-and-audits)).
`mkv-probe`, `mkv-bounds`, `mkv-order`, `mkv-anilist` and `mkv-audit` only read.

`mkv-probe`, `mkv-bounds`, `mkv-split`, `mkv-order`, `mkv-anilist` and `mkv-audit` are
library tools: run them in the library pod (`deploy/mediaops`), as `mkv-manifest` runs. They
carry no role guard, but the ripper mounts only the staging tree. Each prints one JSON value
on stdout for exits 0 and 1 and only a `FATAL` line for 2 to 4.

Also on `PATH`: `mkvmerge`, `mkvpropedit`, `mkvinfo`, `mkvextract`, `ffmpeg`,
`ffprobe`, `mediainfo`, `montage`, `go`, `jq`, `rsync`. There is no Python.

`mkv-order` is the cheapest verification here and worth reaching for first: lay a
disc's episode files end to end and check whether their chapter marks reproduce the
Play-All's. If they do, track order is episode order — proven from the disc's own
authoring, no frames needed. It reads the object `mkv-probe` wrote, one disc per directory,
and exits 1 unless every disc is `confirmed`; a disc with no Play-All says `no_playall`
rather than passing. It still cannot tell you *which* episode the first track
is; that always needs content.

```bash
mkv-probe "$STAGING_ROOT/<category>/Show" > "$PLAN/inventory.json"
mkv-order "$PLAN/inventory.json" "$STAGING_ROOT/<category>/Show/Disc 01"
```

`mkv-anilist` takes a search string or `--id N` and prints the entry as JSON; its episode
titles are ordered but not numbered, so treat their order as advisory. An unknown series is
exit 2. `mkv-audit` checks the library the way the normalisation rules read it, without
writing: `series` the promoted-series rules, `films` the `Movies/` convention, `blindspot`
the media an `.mkv` filter hides, `sweep` a film manifest before it runs, and `postcheck` a
manifest run after it ([`SPEC.md` §13](SPEC.md#13-play-all-analysis-splitting-and-audits)).
A directory or root that does not exist is exit 2, not an empty series.

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
card", the worst possible failure for an identity check. `--band` (`0.55`, `0.30:0.70`, …)
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

`mkv-cards` finds a held title card when it moves around the episode. `find` looks for runs
of 1 fps samples that keep the card's colour (Berserk: `--maxy 25 --minv 127 --static`;
Cutie Honey: `--lo 90 --hi 420 --minv 160 --maxu 115`), and `freeze` for spans
`freezedetect` calls frozen whose colour fits. Both report **every** candidate, not one
winner, and `mkv-cards sheet --candidates` draws one tile per candidate so the card is read
off the page: a detector that picks one run chose the next-episode preview's white on
Penguindrum. A file with no candidate is not a pass; it falls back to a sheet.

Every sheet is decoded by ffmpeg and composed and labelled in Go, so its labels do not
depend on fontconfig. The tools write only the `OUT.png` or fingerprint files you name
([`SPEC.md` §15](SPEC.md#15-frame-evidence-sheets-fingerprints-and-title-cards)).

Check what a disc's short tracks actually *are* before filing them. On one pass this
caught three Funimation trailers for entirely unrelated shows sitting inside a series'
Extras folder.

## Ripping discs

`makemkvcon` is built into the image (a self-contained glibc bundle under
`/opt/makemkv`, since the image itself is musl), and `mkv-drives` / `mkv-rip` drive
the eight BP60NB10 drives on `styxeon`.

**Run them in the ripper pod only:**

```bash
kubectl -n mediaops exec deploy/mediaops-rip -- mkv-rip list
```

Both tools refuse to run, exit 3, unless `MEDIAOPS_ROLE=rip`, which only the ripper's
Deployment sets (`--version` works anywhere). The reason is the lease below: it lives in
one pod's `/tmp`, so `mkv-rip list`, `status` or `wait` run in the library pod would
read a live rip as dead and write that verdict to the NAS.

**The ripper pod must be privileged** to see `/dev/sr*` and `/dev/sg*`, with `cdrom` (24) in
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
mkv-rip identify                 # read the title table of every loaded drive, one at a time
mkv-rip start plan.json          # prints the run id; the rip survives a dropped exec ("-" reads stdin)
mkv-rip status [run-id]          # per-disc state and progress; no id = the run holding the drives
mkv-rip wait <run-id>            # blocks; exits 0 only if every disc is done
mkv-rip list                     # every run, newest first
mkv-rip finalize <run-id>        # re-verify a finished run's failed discs still in their drives
mkv-rip stop <run-id>            # stop the run holding the drives
```

One controller at a time: whoever may spin a drive (a rip, a `finalize`, an `identify`)
holds a lock in the ripper pod, and a second one is refused with exit 3, naming the holder.
Every command prints one JSON value; errors are one `mkv-rip: FATAL:` line.

Rips land in `$STAGING_ROOT/` as `<dest>.partial/` and are renamed into
place only after the run passes: makemkvcon exit 0, no read errors, every file's
duration within 2% of what MakeMKV reported. A `manifest.json` records every title, so
classification can read it instead of re-probing. Each disc is first read with
`makemkvcon info` (its scanning slot covers both commands), because `mkv` mode prints no
title table and there would be nothing to check the files against; if that table and
the rip disagree about which playlist a title is, the disc fails. `mkv-rip finalize`
re-runs that check and the promotion for a run's `failed` discs whose `.partial` and
disc are still there. The plan format and the full rules
are in [`SPEC.md` §10](SPEC.md#10-disc-ripping-mkv-drives-and-mkv-rip).

The licence key arrives as `$MAKEMKV_KEY` and is written to `$HOME/.MakeMKV/` on the
`/work` emptyDir. It never touches the NAS. Run state is the opposite: it lives on the
NAS at `$STAGING_ROOT/.rips/<run-id>/` (state, plan, controller log, per-disc rip and info logs),
so a pod roll cannot lose the record of what was ripped. `mkv-rip` refuses to run if the
staging tree is not a real mount.

## Development

Every tool is Go: one command per directory under `cmd/`, with shared code in `kit/` and
`internal/`.

```bash
go vet ./... && go test -race ./...        # no hardware, network or key needed
CGO_ENABLED=0 go build -o /tmp/out ./cmd/...
```

The tests replay `makemkvcon` output recorded from real discs
(`internal/makemkv/testdata/`) and re-execute the test binary as the fake `makemkvcon`.
CI runs `gofmt`, `go vet`, a pinned `staticcheck` and the tests before it builds an image,
and a failing test blocks the push. Any merge to `master` rolls the pod, which kills a
running rip: land only when `mkv-rip list` (in `deploy/mediaops-rip`) shows nothing running.

## One-off programs

The image carries the Go toolchain for the throwaway analysis an agent used to write in
Python. Copy the job template next to the evidence it will produce and run it; nothing is
fetched, so it works with the network off:

```bash
cp -r /opt/mediaops/src/kit/template "$PLAN/src/seamcheck" && cd "$PLAN/src/seamcheck"
go mod tidy && go run . ARGS
```

The template imports `kit/`, the packages the tools are built from (`proc`, `ffprobe`,
`frames`, `mkvtool`, `fsutil`, `paths`). Anything outside the standard library, `kit` and
`golang.org/x/sys` fails to compile rather than download. The compile cache is on `/work`,
so the first run after a pod roll pays a few seconds and later ones are sub-second.
`go run` turns every non-zero exit into 1; when the exit code matters, `go build -o
/tmp/<name> .` and run the binary. See
[`SPEC.md` §14](SPEC.md#14-the-go-toolchain-as-the-ad-hoc-runtime).

## Running a manifest

`mkv-manifest` takes a manifest of ops (`rename`, `archive_file`, `remux`, `title`, `flags`,
`audio_lang`, `video_lang`, `rmdir_if_empty`, and the rest in [`SPEC.md` §12](SPEC.md#12-manifest-runs-mkv-manifest))
with absolute paths: `MEDIA_ROOT` for everything but archive destinations, which must lie
under `ARCHIVE_ROOT`. Write it into the job's plan dir on the NAS, because the journal is
written beside it and that is what lets a rerun resume after a pod roll. Run it in the
library pod (`deploy/mediaops`); its lock lives in that pod's `/tmp`, and the ripper
mounts only staging:

```bash
mkv-manifest gen rename-series /mnt/media/<box>/<category>/Show Show > "$PLAN/rename.json"   # or write one by hand
mkv-manifest check "$PLAN/rename.json"        # every finding at once; exit 1 if any
mkv-manifest exec "$PLAN/rename.json"         # dry run: check, then the plan; touches nothing
mkv-manifest exec "$PLAN/rename.json" --go    # check again, then perform, journaled
```

A failure or a stopped run leaves the journal behind; run the same `exec --go` again and it
skips what is done and settles whatever was in flight from what is on disk. Edit the
manifest after ops were applied and it refuses, because the journal belongs to the old
one. A long run should survive a dropped `kubectl exec`:

```bash
setsid nohup mkv-manifest exec "$PLAN/rename.json" --go </dev/null >"$PLAN/run.log" 2>&1 &
```

## Splitting a Play-All

Probe the disc, find the boundaries, look at one, then split. Run it in the library pod:

```bash
mkv-probe DISC_DIR > "$PLAN/inventory.json"       # one object; exit 1 if any file failed to probe
mkv-bounds DISC_DIR/title_t00.mkv 24               # the hint (episode minutes) is required
mkv-frames DISC_DIR/title_t00.mkv "$PLAN/b1.png" 1420 1424 1425 1426 1430   # eyeball one interior boundary
mkv-split DISC_DIR/title_t00.mkv "$OUT/Show S01 D01" 6,10,14,18,23        # dry run: the plan and its prechecks
mkv-split DISC_DIR/title_t00.mkv "$OUT/Show S01 D01" 6,10,14,18,23 --go   # split, place, verify
```

`mkv-bounds` takes the episode length as a hint because without one a Play-All of 24-minute
episodes also fits as twice as many half-episodes. Its `split` field is the `chapters:…`
argument and `split_chapters` the same numbers as a list, for `mkv-split`.

`mkv-split` reads the source's own packets around every cut before it writes anything. A cut
must land on a keyframe within `--tol` (default 0.042 s, one frame at 24 fps), and the
report names the nearest keyframe on either side when it does not. For a chapter that is the
first keyframe at or after the chapter's start, because mkvmerge cuts there: a keyframe a few
milliseconds before the chapter would put the cut a whole GOP late, so it is a finding.
A keyframe that opens a GOP (pictures after it are shown before it) is a finding too, unless
`--allow-open-gop` records that you accept it. With `--at`, the cut is the keyframe nearest
each time on either side, and mkvmerge is given that keyframe's own timestamp rather than the
one requested; that replaces anime-normalize's `mkv-split-ts.py`.

`--go` writes `PREFIX-NNN.mkv.partial`, places each part without overwriting, and then runs
`verify`, which is part of the command and not optional. The source is never written,
renamed or removed, and no part is removed on any failure. Any `PREFIX-NNN.mkv` already
present is exit 3. `verify` proves the split, and reads only:

```bash
mkv-split verify SRC PART PART...        # parts in order; exit 1 on any finding
```

It checks that every packet of every stream is in exactly one part, in order (video compared
with the access-unit delimiters that mkvmerge drops removed on both sides; PCM, which mkvmerge
re-frames, compared as one byte stream), that timing
survives, that every part decodes, that the frames at each seam are the source's, and that the
parts total 99.0-100.5 % of the source's bytes. Container durations are reported and never
judged: mkvmerge zeroes each part on its earliest presentation time.

## The one thing to remember

`mkv-bounds` **cannot tell you a cut lands in the right place.** A set of cuts
shifted by a constant offset has the same stride, the same spread, the same fit
error and the same episode length as the correct set. No statistic separates
them — this was confirmed on a track that graded `auto` at fit error 0.226 and
cut 1.57 minutes *inside* every episode. The `limit` field printed with every
pattern says so, and `grade: auto` means the track looks like a Play-All, not that
the anchor is right.

Only frames can settle it. Run `mkv-frames` on one interior boundary per track
before splitting. Of the tracks that were actually checked, roughly a third were
anchored wrong. `mkv-split`'s keyframe precheck proves a cut is *clean*, not that it is
*correct*: a constant offset can land every cut on a keyframe.

The structure to read for:

```
ED credits → (post-ED bumper) → next-episode preview → CUT → cold open → OP → title card
```

The 次回予告 next-episode preview belongs to the **outgoing** episode. Anchoring
on its title card puts every cut one chapter late.

## Working state

Tools live in the image; **working state lives on the NAS** under
`$STAGING_ROOT/<category>/_plans/` (`bin/`, `state/`). Iterating on a plan therefore never
requires an image rebuild, and nothing important is ever kept in a scratchpad —
one session lost its entire working set to a `/tmp` clear.

This matters more than it looks. The deployment is `strategy: Recreate` with Flux image
automation, so **pushing a new image redeploys the pod and kills anything running in it**,
and `/work` is an `emptyDir` that goes with it. Long jobs must write to the NAS, and
should be resumable — `mkv-fingerprint batch` skips work already on disk for exactly this
reason.
