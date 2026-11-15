# SPEC: mediaops — a permanent media-processing workbench on the cluster

**Status:** IMPLEMENTED and deployed. Measured results in §9.
**Drafted:** 2026-09-02
**Deliverables:** a new image repo `Reclyptor/MediaOps` → `ghcr.io/reclyptor/mediaops`,
and a new Flux app `apps/mediaops` in `Reclyptor/Kubernetes`.

---

## 1. Purpose

Give media-library work a permanent home **on the cluster, wired to the NAS**,
instead of running it from the workstation.

The immediate driver is the `_pending/anime` backlog: 39 entries, 1,100 mkv,
4.96 TiB, of which 121 MakeMKV "Play-All" tracks (3.15 TiB) must be split into
~770 episodes with `mkvmerge --split chapters:`. Measured throughput from the
workstation is **12.5 MB/s** — its only live NIC is WiFi (`wlp134s0f0`; both
Ethernet ports are down, no carrier). That is **~72 hours** of wall clock.
`styxeon` has a wired 10G path to the NAS at `192.168.1.2`.

But the backlog is a *first* job, not the only one. Renames, container-title
fixes, archive-then-promote moves, chapter inspection, contact-sheet frame
verification and metadata lookups all recur every time discs are ripped. So this
is specified as a **standing workbench**, not a one-off Job: a long-lived pod
with the media volume mounted and the right tools on `PATH`, that survives
reboots and is there the next time it is needed.

### Non-goals

- **Not a scheduler or a service.** No HTTP surface, no queue, no controller. It
  is a place to run commands, driven interactively (`kubectl exec`) or by a
  batch script. Adding orchestration before there is a second consumer would be
  speculative.
- **Not a transcoder.** Every operation in scope is a stream copy or a probe.
  No GPU, no re-encode. (`anime-restore` is the project that needs GPUs; it is
  separate and out of scope here.)
- **Not a replacement for `seraex`.** That app owns its own pipeline directories.

---

## 2. Hard constraints

Carried forward from `docker/anime-restore/SPEC.md`, which records these as
standing user directives. **C4–C6 there were scoped to that read-only
restoration project and do not transfer verbatim** — this workbench exists
precisely to reorganise the library, so it must be able to write. What does
carry is the discipline around *how*.

| # | Constraint |
|---|---|
| M1 | **No media I/O through the workstation.** It is on WiFi. Pods mount NFS themselves. (= C8) |
| M2 | **Everything declarative, via the Flux GitOps repo.** No `kubectl run`, no hand-applied manifests. (= C9) |
| M3 | **No destructive operation on media, ever, unless explicitly requested in that moment.** Prior approval never carries forward. (= C7) |
| M4 | **Never delete, never overwrite.** Supersede by archiving first, then promoting. Per the `anime-normalize` skill. |
| M5 | **Files created on the NAS must be `3000:100`,** matching every existing file under `/mnt/primary/videos` (mode 770, group `users`). |

> M3 and M4 are the ones that matter. The pod has read-write access to the whole
> media root — that is a loaded gun, and the mitigations below are structural,
> not procedural.

### How M5 is enforced structurally

`apk` needs root; media files must be owned by 3000. An earlier hand-applied
attempt tried to do both in one container and crashlooped
(`ERROR: Unable to lock database: Permission denied`).

Baking the toolchain into an image at build time removes the conflict entirely:
**no process in the running pod is ever root**, so a root-owned media file is
not merely avoided by convention — it is unrepresentable. This is the main
reason to build an image rather than `apk add` at startup.

---

## 3. The image

**Base:** `alpine:3.22`. Chosen over Debian because its `mkvtoolnix` and
`ffmpeg` are current rather than a release behind, and because every tool needed
is a stream-copy/probe utility with no heavy runtime.

### Packages

| Package | Why |
|---|---|
| `mkvtoolnix` | `mkvmerge` (chapter splitting — keyframe-exact, stream copy), `mkvpropedit` (container titles), `mkvinfo`, `mkvextract` |
| `ffmpeg` | `ffprobe` for duration/stream/chapter probing; `ffmpeg` for frame extraction |
| `imagemagick` + `font-dejavu` | `montage` and `drawtext` for the skills' bash contact sheets (the Go `mkv-*` sheets label in Go). The font is not optional — `-annotate` silently produces unlabelled tiles without it, and an unlabelled contact sheet cannot be used to verify a cut point |
| `ca-certificates` | AniList GraphQL over TLS (`mkv-anilist`) |
| `bash`, `coreutils`, `findutils`, `grep`, `sed`, `gawk`, `jq` | Alpine's busybox applets differ enough from GNU to break scripts subtly |
| `rsync` | verified copies; `--remove-source-files` only ever under M3 |
| `mediainfo` | second opinion on container metadata when `ffprobe` is ambiguous |
| `tzdata` | timestamps in logs match everything else (`America/Chicago`) |
| `sg3_utils` | `sg_start --eject` and `sg_turs`: tray and media probes that never spin a disc (§10) |
| `lsscsi` | cross-checking the sr/sg pairing when sysfs is ambiguous |
| `util-linux-misc` | `setsid` and `dmesg`: a rip must survive a dropped `kubectl exec`, and the health watch reads the kernel log when it is allowed to |

### User

A `media` user, **uid 3000, gid 100** (`users` — already Alpine's `users`
group), matching the NAS exactly. `USER media` in the Dockerfile so the image
cannot be run as root by accident.

### Baked-in tools → `/opt/mediaops/bin` (on `PATH`)

Every tool is a Go program under `cmd/` (shared code in `kit/` and `internal/`), built into
the image by a `golang` stage. The image has no Python: the ad-hoc runtime is the Go
toolchain (§14), and the build fails if a package pulls `python3` back in.
These were developed against the backlog and are the reason this is an image and
not just a package list:

| Tool | Purpose |
|---|---|
| `mkv-probe` | Bulk inventory → one JSON object: duration, chapter starts, stream layout, per file (§13) |
| `mkv-bounds` | Episode-boundary detection over a Play-All's chapter list; emits the `mkvmerge --split chapters:N,M` argument and its grade (§13) |
| `mkv-frames` | N timestamped frames from a file, tiled into one labelled contact sheet (§15) |
| `mkv-titlescan` | One frame every STEP seconds over a window, tiled; the whole frame by default (§15) |
| `mkv-fingerprint` | dHash fingerprints of tracks (`hash`, resumable `batch`) and preview-to-body adjacency (`match`) (§15) |
| `mkv-dupcheck` | One track's body against every other's: alternate cut or distinct content (§15) |
| `mkv-staticcheck` | Distinct-frame ratio at 1 fps: disc furniture or real content (§15) |
| `mkv-cards` | Title-card candidates by colour (`find`) or freeze (`freeze`), and `sheet`s of candidates, points, chapters or seams (§15) |
| `mkv-split` | Keyframe and open-GOP precheck, `mkvmerge --split` to `.partial` files, then a mandatory `verify`: packet accounting, timing, decode, seams, bytes; `verify` also runs alone (§13) |
| `mkv-order` | Proves a disc's track order is episode order from its Play-All's chapter marks (§13) |
| `mkv-anilist` | Episode count and titles from AniList (§13) |
| `mkv-audit` | Read-only `series`, `films`, `blindspot`, `sweep` and `postcheck` audits (§13) |
| `mkv-promote` | Manifest-driven mover with full accounting (§5, §11) |
| `mkv-archive` | Archive-before-promote into `$ARCHIVE_ROOT/<path within its share>`, preserving the layout the file had (§11) |
| `mkv-manifest` | `check`, `exec` and `gen` for op manifests: an overlay-replay gate that every `exec` runs first, a journaled, resumable executor, dry run by default (§12) |
| `mkv-drives` | One JSON array of the optical drives: serial, device nodes, USB port, link speed, tray/media state (§10) |
| `mkv-rip` | Staggered, health-gated disc ripping into `_pending` with verification and a manifest; `identify`, `finalize`, `stop` (§10) |

**Split of responsibility:** stable tooling is baked into the image; *working
state* — the split manifest, the content-verified corrections, run logs — lives
on the NAS under `_pending/anime/_plans/`, so iterating on a plan never
requires an image rebuild. A one-off program runs from its source in the plan dir
(§14) and is promoted into a tool once it recurs.

> This split is also a direct response to losing the previous session's entire
> working set: it was in `/tmp`, and `/tmp` was cleared. Nothing durable goes
> in a scratchpad again.

### CI

`.github/workflows/build.yml`, copied from `Lucebox-DFlash` and extended. A `test`
job runs on every pull request and every push to `master` touching the image's
inputs (`Dockerfile`, `go.mod`, `go.sum`, `cmd/**`, `kit/**`, `internal/**`):
`gofmt`, `go vet`, a pinned `staticcheck` and `go test -race`. `build-and-push` needs
`test`, so a failure blocks the image. On a pull request it builds without pushing;
on `master` it pushes to `ghcr.io` tagged `latest` and `<timestamp>-<shortsha>`, and
passes that tag to the Go build, where each tool records it as `tool_version`. GHA
cache. Same `docker/metadata-action` + `docker/build-push-action@v6` versions as the
existing repos.

---

## 4. The deployment (`apps/mediaops`)

Mirrors `apps/makotogotchi` and `apps/seraex`.

| File | Content |
|---|---|
| `namespace.yaml` | ns `mediaops`, pod-security `baseline` enforce/audit/warn |
| `pv.yaml` | `mediaops-media-dxp6800` — NFS `192.168.1.2:/mnt/primary/videos`, RWX, `Retain`, SC `nfs-mediaops-media` |
| `pvc.yaml` | `mediaops-media` bound by `volumeName` |
| `deployment.yaml` | replicas 1, `command: ["sleep","infinity"]` |
| `secrets/kustomization.yaml` | patches the shared `ghcr` secret into the ns as `ghcr-secret` |
| `ciliumnetworkpolicy.yaml` | egress only (§4.3) |
| `kustomization.yaml` | ties them together; add `mediaops` to `apps/kustomization.yaml` |

### 4.1 Scheduling and resources

Prefers `styxeon` by `nodeAffinity` — **it is the wired-10G node, and that is
the point of the app.** The ripper pod (`deploy/mediaops-rip`, §10) is the one that
must run there, for the drives. styxeon currently sits at 20% CPU / 22% memory
allocated of 16 cores / 28 GiB, so there is ample room.

```
requests: cpu 500m, memory 1Gi      # idles cheap; it is a workbench, usually asleep
limits:   cpu 12,   memory 16Gi     # bursts wide for parallel mkvmerge
```

Requests are deliberately far below limits: the pod is idle most of the time and
should not hold a reservation it is not using, but when a split batch runs it
should be able to saturate the box.

### 4.2 Volume

One mount **per NAS**, all beneath `/mnt/media`:

| Mount | Export |
|---|---|
| `/mnt/media/dxp6800` | `192.168.1.2:/mnt/primary/videos` |
| `/mnt/media/dxp4800` | `192.168.1.3:/mnt/primary/videos` |
| `/mnt/media/asustor` | `192.168.1.5:/volume1/data` |

**Every entry is a dataset ROOT, and never a child dataset beneath one.** That is
a privacy rule before it is a technical one, and it matches
`NixOS/modules/workstation/nfs.nix` verbatim. The NAS serves each category as its
own ZFS dataset and NFSv4 crosses into an exported child on first access, so
naming the children would buy nothing — they are reachable through the root
either way. What it *would* do is write the full set of category names into a
public repository, and that set is itself the thing worth not publishing. The
same reasoning is why `ARCHIVE_ROOT` arrives from a Secret (§4.2.1).

A whole-share mount per NAS rather than seraex-style per-purpose subpaths,
because the work spans the staging trees, the archive and every category
directory in one operation — an archive-then-promote move crosses three of them.
Splitting it into narrower mounts would not reduce the blast radius, only obscure
it, and would put the category names in this file besides.

**`MEDIA_ROOT` is not itself a mount.** It began as one, back when dxp6800 was
mounted directly at `/mnt/media`, and code written then could synthesize a
sibling path like `MEDIA_ROOT/_archive` and be sure it landed on the share. It
now resolves to the pod's **ephemeral overlay** — writable, silent, and discarded
on the next redeploy, which `strategy: Recreate` plus Flux image automation makes
routine. `mkv-archive` moved files there and reported success; the data survived
only until the next image push.

The rule that follows is enforced in code: **never synthesize an archive path
from `MEDIA_ROOT`.** `/mnt/media` and `/` report the same `st_dev` (134), which is
the whole bug in one number.

### 4.2.1 The archive is one tree, on the box that is not being worked

The archive used to live on the same share as the file, resolved from the file
itself by a mount-point lookup. That made every archive move a rename, but it
also scattered the archive across every NAS and let the working box fill up with
material that is, by definition, never read again. It is now a single tree,
configured by **`ARCHIVE_ROOT`**.

The working categories stay on dxp6800 and the archive lives on dxp4800, and
that split is permanent rather than a migration in progress. **Crossing a
filesystem boundary is therefore the normal case for every archive move, not an
exceptional one** — which is exactly what the previous design refused to do.

`ARCHIVE_ROOT` has **no default**, unlike `MEDIA_ROOT`. The risk is asymmetric: a
wrong `MEDIA_ROOT` finds nothing and fails loudly, while a wrong `ARCHIVE_ROOT`
accepts real data onto the overlay and reports success. `paths.Config.ArchiveRoot`
(`kit/paths`) refuses to proceed unless the variable is set, the directory already exists, its
`st_dev` differs from `/`, and it is writable. It never creates the root — a
`mkdir` would itself succeed on the overlay and look exactly like the correct
outcome.

Its value is carried in a sops-encrypted Secret rather than written into
`deployment.yaml`. The Kubernetes repository is public, and the standing rule
there is to name dataset roots only and never enumerate what sits beneath them —
the set of category names is itself the thing worth not publishing.

**The archive dataset needs its own line in `/etc/exports` on the NAS.** It is a
ZFS dataset, so it is a filesystem of its own, and the kernel NFS server does not
serve a child filesystem through its parent's export. An unexported child is not
an error the client can see: it shows through the parent export as an empty,
`root:root 0755` directory sitting on the right device, which is exactly what the
archive root looked like from inside the pod before the export was added. Every
other child dataset there already has its own stanza, so this is the established
pattern
rather than a new requirement. No matching change is needed on the pod side: the
dataset root is already mounted and NFSv4 reaches an exported child through it,
so adding a volume for the archive would publish its name for nothing.

Note that a mount-point test would *not* catch the stub: the child exports that
do work are not mount points on the client either — they report the same `st_dev`
as their parent and `os.path.ismount()` is false for them. Writability is what
separates the real dataset from the stub, so that is what is checked.

Three consequences:

1. **Archive paths drop the share and keep the category.** A file at
   `<box>/<category>/X/Y.mkv` archives to `<ARCHIVE_ROOT>/<category>/X/Y.mkv`.
   A category lives on exactly one box, so the box name carries nothing the
   archive needs, and omitting it means moving a category between boxes does not
   fork its archive into two trees. The share is taken as `MEDIA_ROOT`'s
   immediate child, not from `share_root()`: whether a category is its own
   client-side mount depends on how the NAS exports it, and that must not decide
   where a file lands.
2. **Crossing a filesystem boundary is now the normal case, not a fault.** The
   old code refused it outright, which was right when a cross-device move could
   only mean the overlay. `mkv-archive` now renames when it can and otherwise
   copies to a `.partial` name, fsyncs, size-checks, renames into place, and
   unlinks the source last — so the file exists whole in at least one location
   at every instant, and an interrupted copy can never be mistaken for a
   finished one.
3. **Free space is checked before a batch runs.** A rename consumed nothing; a
   copy can fill the destination halfway through. `mkv-archive` sums the bytes
   that will actually be copied and refuses the batch if they do not fit.

The mount-point lookup (`paths.Config.ShareRoot`) survives for one job: bounding the upward `rmdir` sweep that
removes directories the archive emptied, so it can never climb out of the share.

`securityContext: runAsUser 3000, runAsGroup 100, fsGroup 100` (M5).

### 4.2.2 Ownership and mode

What lands on disk is decided by two separate things, not by the pod's own uid and gid:

- **The group comes from the NFS export.** The dxp6800 export squashes every client
  to `3000:3000`, so files and directories
  land as `media:3000` whatever uid or gid the writing process has, and the process'
  own group (`runAsGroup: 100`) never reaches disk. No code in the pod calls `chown`
  or `chgrp`: the server decides the owner, and a client-side fix is not possible.
- **The mode comes from the creating process' umask,** and `kubectl exec` runs its
  command directly — not through `CMD` or `ENTRYPOINT` — so the image cannot set it
  centrally. It is set per caller: the Go `mkv-*` tools set it at start (`internal/cli`),
  and `BASH_ENV=/etc/mediaops-profile.sh` for `kubectl exec -- bash -c`, which covers a
  one-off started with `go run` from that shell.
  **Not covered:** `kubectl exec -- sh -c`, since busybox ash reads `$ENV` only when
  interactive. Ad-hoc `sh -c 'mkdir …'` still inherits 022.

That yields 660 files and 770 dirs. It does not reproduce the 770 on existing media
files: umask only clears bits, so nothing it does can add an execute bit that was
never requested. The x bit is meaningless on a `.mkv`; group-write is the part that
matters.

### 4.3 Network policy

Egress only; no ingress, no Service.

- DNS to `host:53`
- `graphql.anilist.co:443` — AniList metadata (**requires a `User-Agent` header
  or it returns 403**)
- `api4.thetvdb.com:443` — TVDB, for non-anime TV

**NFS needs no rule.** The volume is mounted by the kubelet on the node, so
that traffic originates from the host network namespace, not the pod's.

### 4.4 Flux image automation

`flux-system/mediaops-image.yaml` — `ImageRepository` + `ImagePolicy`, same
`^(?P<ts>\d{14})-.+$` numerical-ascending pattern as makotogotchi, and the
`# {"$imagepolicy": "flux-system:mediaops"}` marker on the deployment's image
line. Green master build → auto-deploy.

**One refactor required.** `flux-system/kustomization.yaml` currently patches the
single shared `ghcr` secret to the name `ghcr-makotogotchi`. Kustomize cannot
emit a second differently-named copy of the same resource, so adding a second
`ImageRepository` needs the secret renamed to plain **`ghcr`**, with both
`ImageRepository`s referencing it.

That is the correct shape regardless: the secret is a registry pull credential,
not a makotogotchi-specific one, and its current name misrepresents it. It is a
rename in two files plus one `secretRef` — small, but it touches a working
deployment, so it is called out here rather than done silently.

---

## 5. `mkv-promote` accounting (why it is a script and not `mv`)

Every move is checked before anything is written, then verified after:

1. no source file mapped twice; no two entries sharing a destination
2. **every** on-disk source *video* file appears in the manifest — an unmapped
   video is a hard error, never a silent skip. Non-video is deliberately out of
   scope: a series' `Extras/` legitimately carries soundtrack FLACs, booklet
   scans and cover art (one real series holds 1,297 of them), and demanding
   those be mapped turns a correct plan into a hard failure and pushes the
   operator toward a raw `mv` with no accounting at all — strictly worse. A
   video that genuinely should stay put is declared explicitly with
   `{"keep": "..."}` rather than omitted, so the intent is in the manifest.

   > `VIDEO_EXT` is the definition of "video" here, and an extension missing
   > from it is invisible to this check — the exact failure the step exists to
   > prevent. It is kept in step with the enumeration in anime-normalize's
   > `probe-tracks.sh`. The library is not mkv-only: `.hack//SIGN` is 27 `.ogm`
   > files, and there are `.vob` and `.m4v` holdings besides.
3. no destination path already occupied (M4)
4. batch touch-validation of every destination directory before the first move
5. the move (a rename, or the safe copy of §11 when it crosses a filesystem),
   then byte-size equality source↔destination
6. `ffprobe` decode check on every moved `.mkv`
7. cleanup of source directories only once empty, and never beyond the share
   (§11)

Container titles are set between 6 and 7, on the files that passed 6.

> Step 6 exists because it once silently did nothing: it was written with
> `ffprobe -f null -`, but `-f null` is an **ffmpeg** flag. `ffprobe` exited
> with "Unknown input format: null" on all 44 files, which read like 44
> warnings and actually meant the check never ran. **A checker that fails open
> is worse than no checker** — so `mkv-promote` treats a non-zero probe exit as
> a failure, and asserts a positive duration rather than merely a clean exit.

---

## 6. What this does *not* solve

Recording these so the workbench is not mistaken for a finished pipeline.

**Boundary detection cannot self-verify.** Four successive detector models
failed, and the last two failed *invisibly*:

- A set of cuts shifted by a **constant offset** is exactly as regular as the
  correct set — same stride, same spread, same fit error, same episode length.
  Confirmed on Demon Slayer S02/D02, which graded "auto" at fit error 0.226 and
  cut **1.57 min inside** every episode.
- The 次回予告 next-episode preview belongs to the *outgoing* episode, so
  anchoring on its title card puts every cut one chapter late.

No statistic distinguishes these. `mkv-bounds` screens out tracks that are not
Play-Alls and says so in the `limit` it prints with every pattern; it **cannot** confirm a real Play-All is being cut in the right
place. Only frames can — hence `mkv-frames`, and hence the rule that **one
interior boundary per track is eyeballed before that track is split.** Of the
tracks a reviewer actually checked, roughly a third had a wrong anchor.

**Two decisions remain open** and are not technical: whether to restructure
`Maken-ki!` out of its release-group folders, and whether Haruhi adopts the
discs' 28-episode season or the library's 2006/2009 split.

**Disk space policy is unresolved** and is the one genuinely destructive choice:
6.4 TiB free against ~5 TiB of split output. Archive the Play-Alls (~1.4 TiB
free at the end) or delete each after its split verifies (stays ~6.4 TiB).
Per M3, **nothing gets deleted without an explicit, in-the-moment instruction.**

---

## 7. Open decision for review

**The name.** `mediaops` was chosen over `plexops` because nothing here is
Plex-specific — the same tools serve Emby, Jellyfin, and plain library hygiene.
It sets the GitHub repo (`Reclyptor/MediaOps`), the image
(`ghcr.io/reclyptor/mediaops`), and the namespace. Cheap to change now, annoying
later. Say the word if you want it called something else.

---

## 8. Execution order

1. `Reclyptor/MediaOps`: `Dockerfile`, `scripts/`, `.github/workflows/build.yml`, `README.md` → push `master` → confirm the GHA build publishes to ghcr
2. `Reclyptor/Kubernetes` (in a worktree per topology rules): rename the flux-system `ghcr` secret, add `flux-system/mediaops-image.yaml`, add `apps/mediaops/**`, register in `apps/kustomization.yaml` → rebase onto `master`
3. Confirm Flux reconciles and the pod is `Running` on `styxeon`
4. **Measure NFS throughput from the pod before trusting the 5× estimate** — the
   72 h → 14 h claim is an extrapolation from a workstation-side measurement and
   has not been observed on this path
5. Re-create the lost working set (`splitplan.json`, `corrections.json`) onto the
   NAS — recoverable from the previous session's transcript
6. Only then: spot-check boundaries, split, verify, promote — series by series

---

## 9. Measured results (2026-09-03)

All six steps of §8 are done. Numbers observed on the deployed pod, not estimated.

### Throughput

| Path | Sequential read | Real split, end to end |
|---|---|---|
| Workstation (WiFi) | — | **12.5 MB/s** source (18.5 GiB in 1481 s) |
| Pod on `styxeon` (wired) | **618 MB/s** (8 GiB `dd`) | **183 MB/s** source (36.4 GiB in 198 s) |

**14.6× faster end to end.** The `72 h → 14 h` figure in the earlier plan was an
extrapolation from a workstation-side measurement and was too pessimistic: the
full 3.15 TiB now projects to **~5.3 hours**, not 14.

The gap between 618 MB/s read and 183 MB/s split is expected — a split reads and
writes the same volume, so combined I/O is ~366 MB/s, and mkvmerge is
single-threaded per job. Running several series concurrently should push the
aggregate higher; styxeon has 16 cores and the limit is well above one job.

### Split correctness

Fate/Apocrypha S01D01, 36.410 GiB, `chapters:6,10,14,18,23` → 6 parts:

```
23.75 min each, 5.951 / 6.087 / 6.072 / 6.085 / 6.100 / 6.095 GiB, all clean
parts total 36.391 GiB = 99.947% of source
```

Six episodes within 0.00 min of each other. The 0.053% shortfall is one set of
MKV headers becoming six, plus the dropped Play-All chapter table.

**The anchor was confirmed on frames, not on statistics.** At the first interior
boundary: 1420 s is a night interior (episode 1), **1425 s is the black
end-of-episode copyright card** (©東出祐一郎・TYPE-MOON / FAPC), and 1432 s is a
daytime cold open. The cut lands exactly on the card. This is the check §6 says
cannot be skipped, and it is cheap now — a contact sheet renders in seconds.

Source left untouched; test output removed.

### One defect found and fixed

`mkv-frames` emitted "Fontconfig error: No writable cache directories" on every
call. The labels still rendered, which is precisely the hazard — pages of benign
noise are what a real error hides in. The image gives `media` no home directory,
so the deployment now sets `HOME=/work` and `XDG_CACHE_HOME=/work/.cache` against
the existing emptyDir. Zero errors after the change.

---

## 10. Disc ripping: `mkv-drives` and `mkv-rip`

Added 2026-11-09. Before this, every rip went through the MakeMKV web GUI by
hand, one pod per drive, each scanning whatever disc it noticed.

### Why it is staggered

The eight BP60NB10 drives on `styxeon` share one powered USB 2.0 hub tree
(`usb5` → `5-1`). Spinning several up together has knocked drives off the bus:
at boot on 2026-11-09 the `34d1:0609` sub-hub at `5-1.2` threw `error -71` and
its three drives fell to 12 Mbit/s. There is no power sensor to ask — the hubs
report no current and `over_current_count` reads 0 everywhere — so the gating is
done in software, from MakeMKV's progress and from what the USB tree reports.

### MakeMKV in the image

`makemkvcon` is closed-source and glibc-only; the image is musl. The Dockerfile
therefore has a `debian:12` builder stage (the approach `jlesage/docker-makemkv`
uses): build `makemkv-oss` as libraries only (`--disable-gui`), unpack
`makemkv-bin`, accept its EULA non-interactively, copy in the glibc runtime and
every shared library `ldd` reports, and `patchelf` each ELF so its interpreter and
rpath point at `/opt/makemkv/lib`. The Alpine stage copies that tree in whole.

- The version and the sha256 of both tarballs (and of the ffmpeg source) are build
  args. A changed tarball fails the build rather than shipping.
- `libmakemkv` links `libavcodec` unconditionally, but only its audio-conversion
  path uses it and a rip never converts. A minimal static ffmpeg (no programs, no
  autodetected libraries) satisfies the link without bundling ~100 codec libraries.
- A build-time smoke test runs `makemkvcon -r info disc:9999` and requires the
  `MSG:1005` banner. It exits cleanly with no drives, so it proves the patched
  loader and every bundled library resolve on musl and that the EULA was accepted.
- MakeMKV 2.0.0 is currently labelled a beta on makemkv.com. A purchased key is
  unaffected by beta expiry, but a bump to a stable release should be taken when
  one appears.

### Privilege

The ripper pod (`deploy/mediaops-rip`) must be **privileged** to see `/dev/sr*` and
`/dev/sg*`, and pinned to `styxeon`. It still runs as uid 3000; privilege exposes the device nodes and
grants a non-root process no capabilities, so root-owned media stays
unrepresentable (M5). `supplementalGroups` must include 24 (`cdrom`), which owns
the drive nodes. Group 6 (`disk`) must not be added.

Unprivileged, `mkv-drives` still resolves every serial and link speed from sysfs
but reports `tray`/`media` as `null` with an `error`, and `mkv-rip` blocks every
disc rather than guess.

The key reaches `makemkvcon` as `$MAKEMKV_KEY` (a Secret). `mkv-rip start`, `identify`
and `finalize` write `$HOME/.MakeMKV/settings.conf` from it with mode 600 and
`app_DataDir=$HOME/makemkv`. `HOME` is the `/work` emptyDir, so the key never touches
the NAS and is never logged; a key holding `"`, `\` or a newline is refused.

### `mkv-drives`

Prints one JSON array of the optical drives, in sr order:

```json
[{"serial": "BP52E25512093803", "dev": "/dev/sr0", "sg": "/dev/sg0", "usb_port": "5-1.2.4",
  "link_mbps": 480, "status": "tray_open", "tray": "open", "media": false, "error": null}]
```

It reads `/sys` and asks each drive for its tray state with `CDROM_DRIVE_STATUS` on a
non-blocking fd, which does not spin a disc up and is safe at any time. `sg_turs` on the
sg node is the fallback when the sr node is not visible; if neither answers, `status` is
`unknown`, `tray` and `media` are `null` and `error` says why, rather than a guess. The USB
device behind a drive is the nearest ancestor of `/sys/class/block/srN/device` holding
both `serial` and `speed`; its basename is `usb_port` and its `speed` is `link_mbps`.
`status` carries the raw state, because `not_ready` (a drive still becoming ready after
a disc goes in) is not `no_disc`. With no drives it prints `[]`, a note on stderr, and
exits 0. There is no `label`: reading a disc's name touches the disc, so `mkv-rip
identify` does it, under the drive lease.

### `mkv-rip`

```
mkv-rip start PLAN.json|-        validate, detach a controller, print {"run_id": ...}
mkv-rip identify [SERIAL...]     detach a staggered `info` scan of loaded drives
mkv-rip status [RUN_ID]          a run's state (default: the run holding the drives, else the newest)
mkv-rip wait RUN_ID              block until the run is terminal; print its final state
mkv-rip list                     every run, newest first
mkv-rip finalize RUN_ID [--dry-run]   re-verify a run's failed discs
mkv-rip stop RUN_ID              SIGTERM the controller holding the drives for that run
```

Every command prints exactly one JSON value on stdout and errors as one line,
`mkv-rip: FATAL: <sentence>`, on stderr. Exit codes: 0 success (for `wait`, the run ended
`complete`); 1 the run ended `incomplete`, or `finalize` has nothing to do; 2 usage error,
invalid input or unknown run id; 3 refused by a guard (the drives are leased, and the
message names the holder; staging is not the NAS; `MAKEMKV_KEY` is missing; the role
guard below); 4 internal error. `--selftest` is gone: the tests are `go test`.

**Where the disc tools run.** Library work and ripping are separate Deployments of this
image: `deploy/mediaops` (library) and `deploy/mediaops-rip` (ripper). `mkv-rip` (every
subcommand, including the internal controller) and `mkv-drives` refuse, exit 3 and one
`FATAL` line naming `kubectl -n mediaops exec deploy/mediaops-rip -- <tool> ARGS`, unless
`MEDIAOPS_ROLE` is exactly `rip`; only the ripper's Deployment sets it. The check is
`cli.RequireRipRole`, called first in each command, before any run-store, lease or
device access. `--version` is answered before the command runs and works anywhere; the
tools have no other help output. The guard exists because the lease below is per pod
(see next paragraph): in any other pod `Store.Load` would see no held lease and persist
"controller died" for a live run.

**One controller at a time.** Whoever may spin a drive (a rip, a `finalize`, an
`identify`) holds an exclusive `flock` on `/tmp/mkv-rip.lease` for its whole life and
writes `{kind, run_id, pid, started_at}` into it, so a refusal can name it. Liveness is
the held lock, never a pid: the kernel releases the lock the instant its holder dies,
and a pod roll leaves an empty `/tmp`. The lease is per ripper pod by design; it cannot
see a controller in another pod, which is why the role guard exists. A `running` or `finalizing` run whose lease is not
held and naming it is dead: it is reported `incomplete` / "controller died", its
unfinished discs `failed`, and that verdict is written to `state.json`.

**Detaching.** `start`, `identify` and `finalize` validate in the foreground, then
re-execute themselves as a controller in its own session (`setsid(2)`), with stdio on
`/dev/null`, so a dropped `kubectl exec` does not kill it. The controller acquires the
lease itself, creates the run directory, and reports back over a pipe: the foreground
prints the run id, or the refusal and its exit code. A refused start leaves nothing
behind. `makemkvcon` children carry `Pdeathsig: SIGKILL`, so a killed controller does
not leave a process reading a drive.

**State.** `$MEDIA_ROOT/dxp6800/_pending/.rips/<run-id>/` on the NAS holds `plan.json`
(the validated request, immutable), `state.json` (schema 2, the only mutable file),
`controller.log`, and `disc-NN/` with `info.log`, `rip.log` and one
`finalize-N.info.log` per finalize attempt; nothing is overwritten. It is not on the
`/work` emptyDir because a pod roll would discard the only record of which discs were
ripped. Every command refuses when the staging tree is missing, is not writable or
shares a device with `/`, and the tree is never created. State files are 0660. Only
directories whose `state.json` says `"schema": 2` are runs; everything else under
`.rips/` is left alone, and the Python-era runs are not read.

`status` prints `state.json` as stored, with `controller.alive` filled in:

```json
{"schema": 2, "run_id": "...", "kind": "rip", "state": "running", "reason": null,
 "started_at": "...", "finished_at": null, "tool_version": "...",
 "controller": {"pid": 412, "alive": true},
 "health": {"dmesg": {"available": false, "detail": "..."}, "paused_until": null,
            "pause_reason": null, "events": []},
 "discs": [{"index": 0, "serial": "...", "label": "...", "dest": "...", "state": "ripping",
            "reason": null, "drive": {"dev", "sg", "usb_port", "link_mbps"},
            "progress": {"phase", "current", "total"}, "titles": {"done": 3, "total": 8},
            "makemkv_exit": null, "errors": [], "suspect_events": [], "eject": null,
            "identified": null, "started_at": "...", "copy_started_at": "...",
            "finished_at": null}],
 "problems": []}
```

`run.state` is `running`, `finalizing`, `complete` or `incomplete`; `reason` is set for
`incomplete`. A disc `reason` is a sentence for every blocked, failed and suspect disc.
`eject` is `null` or `{ok, detail}`; `identified` is filled only by `identify`: `{name,
volume_label, type, titles: [{id, duration, duration_seconds, chapters, source_playlist,
size_bytes}]}`. `list` prints `[{run_id, kind, state, started_at, finished_at}]`.

**Plan.** Unchanged: `{"discs": [...], "min_title_seconds": 60, "max_concurrent": 3,
"spinup_gap_seconds": 30, "health_pause_seconds": 120, "scan_timeout_seconds": 900}`, the
last five optional. A series disc is `{serial, category: anime|tv|hentai, series, season,
disc}` and a movie `{serial, category: "movies", title}`; `season: null` is allowed only
for hentai or a disc carrying `container_title`. Unknown keys, duplicate serials and
duplicate destinations are rejected, and the message names the entry. Destinations, under
`$MEDIA_ROOT/dxp6800/_pending/`: `<category>/<Series>/Season XX/Disc XX/` (hentai with a
null season drops `Season XX`), `movies/<Title (Year)>/`. Only `/` is sanitised in
directory names, to ` - `.

**Pre-flight** blocks a disc, with a reason, if its serial is unknown, its drive state
cannot be read, it has no media, its link is not 480 Mbit/s, or its destination (or a
`.partial` of it) already exists. It is checked when the run starts and again just before
each disc spins up. A drive still `not_ready` (as it is for a few seconds after a disc
goes in) is re-checked on later ticks for up to 20 s, without pausing the controller's
health polling, before the disc is blocked. Nothing is ever overwritten.

**Title info pre-pass.** `makemkvcon ... mkv` prints no `TINFO` lines at all: it
announces only `MSG:3307 "File 00009.mpls was added as title #0"`, plus progress. A
parser fed only that run has no title table, so there would be no lengths or output
names to verify files against. So, while the disc holds the scanning slot, each disc is
first read with `makemkvcon -r --noscan --minlength=<s> info dev:/dev/srN`, and that
table (`TINFO` 9 length, 16 source playlist, 27 output name; `CINFO` label) is what the
rip is checked against. The slot and `scan_timeout_seconds` cover both commands. If the
info pass exits non-zero, reports a fatal message or read errors, or lists no titles, the
disc fails without being ripped (hours of copying that cannot be verified are not worth
starting). After the rip, every title id it announced via `MSG:3307` must name the same
source playlist in the table; title ids are positions in a list each invocation builds on
its own, so a disagreement (a different disc, a changed playlist set) would verify files
against the wrong lengths, and fails the disc with a sentence naming the title and both
playlists.

**Rips** run `makemkvcon -r --noscan --progress=-same --minlength=<s> mkv dev:/dev/srN
all <dest>.partial`. `--noscan` stops makemkvcon from touching every other drive during
its startup drive scan, which would defeat the stagger. The command's exit status is
stored on the disc as `makemkv_exit`, so a later re-verification has it.

**Staggering.** A start needs all of: fewer than `max_concurrent` rips running; no drive
in `scanning`; `spinup_gap_seconds` since the previous drive left `scanning`; and no
health pause. `scanning` becomes `ripping` on the progress text, not the numeric codes: a
`PRGC` named `Saving to MKV file` under a `PRGT` beginning `Saving`. The `Opening ...
disc` PRGCs (`Scanning contents`, `Processing AV clips`, `Processing BD+ code`, `Loading
playlists`) and `Analyzing seamless segments` are all still analysis — the drive is not
yet streaming — so they keep the slot held. A disc stuck scanning past
`scan_timeout_seconds` (900) is killed so it cannot starve the run.

**Health watch.** Primarily sysfs, because `dmesg` is normally unreadable for uid 3000
(`dmesg_restrict`): a drive's link speed dropping, its USB device disappearing, or its
sr node disappearing is an event. New kernel lines naming a port on the same USB bus
(`error -71`, `USB disconnect`, `reset ...-speed USB device`, `device descriptor read`,
`new full-speed`, `over-current`) are events too, when `dmesg` is readable; `status` says
`health.dmesg.available: false` with the error text when it is not. Any event pauses new
starts for `health_pause_seconds` (120). An event on a port that is, or is an ancestor
of, a running drive's port marks that disc `suspect`.

**Success** needs all of: makemkvcon exits 0 and the title info pass agrees with it
(above); no read/write-error MSG lines (those whose format is `Error '%1' occurred while
...`, a SCSI error, `Failed to save title`, or corrupt-source and hash-check messages);
makemkvcon's own copy tally (`"%1 titles saved"`, plus `"%2 failed"` when present, read by
format string) present, with no failed titles and a saved count equal to the number of
`.mkv` files, so a rip that exits 0 without a tally fails; every file mapped to a
reported title with an ffprobe duration within 2% of that title's reported length (floor
of 2 s, since reported lengths are whole seconds); and every reported title of at least
`min_title_seconds` present as a file. Anything that cannot be read counts as a failure.
Then segment titles are set with `mkvpropedit` and read back, `manifest.json` is written,
the folder and files get modes 0770 and 0660 (makemkvcon creates files 0666 whatever the
umask), `.partial` is renamed into place, and the tray is ejected: `sg_prevent --allow`,
`CDROMEJECT`, then the tray state is read back, with `sg_start --eject` as the fallback.
An eject failure is recorded on the disc but does not fail a verified rip.

**Failure** leaves the `.partial` folder for inspection and reports why. A `suspect` disc
whose rip otherwise verifies is also left as `.partial`, with its manifest, for a human to
judge — a USB reset mid-rip is not something to promote automatically.

**`mkv-rip finalize <run-id> [--dry-run]`** re-verifies a finished run's `failed` discs,
for a run that failed verification wrongly or whose rip is good but was never promoted.
It takes the drive lease like any other controller, so it is refused while anything else
holds the drives, and a run or an `identify` is refused while it works; both refusals name
the holder. It detaches and prints the run id; `wait` follows it. A disc qualifies when its
`makemkv_exit` is recorded and 0, its `.partial` and `disc-NN/rip.log` still exist, and its
drive (matched by serial) holds media and links at 480 Mbit/s. One disc at a time, with
`spinup_gap_seconds` between drives, it runs a fresh info pass into its own
`finalize-N.info.log`, re-parses the saved `rip.log`, and runs the same verification and
the same tail (titles, manifest, modes, rename, eject) as a normal finish, through one
function. Suspect discs stay `suspect`, and a destination collision still fails. The run
state is recomputed at the end. `--dry-run` prints `{"would_finalize": [...], "skipped":
[{index, serial, reason}]}`, touching nothing, and exits 1 when nothing qualifies.

**`mkv-rip identify [SERIAL...]`** reads each loaded drive's title table without ripping:
with no serials, every drive holding media at 480 Mbit/s, one at a time with
`--spinup-gap-seconds` between them, the others recorded as `blocked`. Each disc ends
`done` with `identified` filled in; it never ejects and writes nothing to the library.
Flags: `--min-title-seconds 60`, `--spinup-gap-seconds 30`, `--scan-timeout-seconds 900`.

**`mkv-rip stop <run-id>`** sends SIGTERM to the controller holding the drives for that
run. No disc starts afterwards, `makemkvcon` is terminated, `pending` discs become
`failed` ("run was stopped before this disc started"), active discs `failed` ("run was
stopped mid-rip (kept at ...partial)"), and the run is `incomplete` with reason `stopped`.

**Titles.** Segment titles follow anime-normalize, so a ripped disc needs no retitling
there: every title on a series disc (episodes and extras) gets the canonical series name,
and a movie gets `<Title (Year)>`. This is the container title field of the MakeMKV GUI.
Episode names stay with anime-normalize, which puts them in filenames once the order is
verified by dialogue. A film that ships in a series box set is planned with the series,
`season: null` and its own `container_title` (`Series: Film (Year)`), which lands it at
`<category>/<Series>/Disc XX/` titled as a movie. Segment titles are never altered.

The run is `complete` only when every disc is `done`; otherwise `incomplete`, with a
`problems` list of the discs that are not.

**Tests.** `go test -race ./...` needs no hardware, network, root or key. The recorded
`makemkvcon` output of the 2026-11-09 run (four real discs, `PRGV` lines thinned) is
`internal/makemkv/testdata/`; a handful of hand-written lines stand in for failures no
real disc has produced yet. End-to-end tests re-execute the test binary as a fake
`makemkvcon`, `ffprobe` or `mkvpropedit`. CI runs them before any image is pushed.

**`manifest.json`** in each ripped folder: `{schema, run_id, disc_index, plan, drive,
makemkv: {version, exit, titles_saved}, disc: {name, volume_label, type}, titles: [{id,
name, file, chapters, duration_seconds, duration, size_bytes, source_playlist, file_bytes,
file_seconds, segment_title}], errors, suspect_events, started, copy_started, finished}`.

## 11. Library moves: `mkv-archive` and `mkv-promote`

Go since the second step of the rewrite. Same manifests, same names, same dry-run by
default and `--go` to act; what changed is that each prints one JSON report, refuses
more, and moves files without ever having fewer than one whole copy.

```
mkv-archive MANIFEST.json [--go]    manifest: [{"path": "<MEDIA_ROOT-relative>"}, ...]
mkv-promote MANIFEST.json [--go]    manifest: [{"src", "dst", "title"?}, {"keep": ...}, ...]
```

Paths are relative to `MEDIA_ROOT`. An absolute path, or one that leaves `MEDIA_ROOT`, is
refused (the Python joined whatever it was given, so an absolute path silently replaced
the root). An unknown key in an entry is an error naming the entry, not a silently
ignored typo. A `keep` cannot be combined with `src`, `dst` or `title`, and a path that
is both promoted and kept is refused.

**Exit codes** are the shared set: 0 done (or a clean dry run); 1 the moves happened but
the decode check failed; 2 a bad manifest, a missing source, a double mapping, an
unaccounted video; 3 a refusal (`ARCHIVE_ROOT` unusable, a destination occupied, a
destination that cannot be created, the copies not fitting); 4 an I/O failure.

**Output** is one JSON report: `dry_run`, `files`, `bytes`, `copied_bytes` (what a
cross-filesystem move would copy), every move with its `bytes` and whether it was
`moved`, and the directories swept. A refusal before anything moved prints only the
`FATAL` line. A failure after the first move prints the report with `error` set and the
moves already made marked, because those files are in the library and the operator needs
to know which.

### The move

`internal/library` moves a file by renaming it. Across filesystems, which is the normal
case for the archive, it copies to `<dst>.partial`, fsyncs, gives the copy the source's
mode and times, and checks that it is as long as the source and that the source did not
change while it was read. It then renames the copy into place, syncs the directory, and
only then unlinks the source, and the unlink itself re-checks that the destination is a
regular file of the expected size. At every instant at least one whole copy exists. A
failure before the rename leaves the source untouched and removes the `.partial`; a
failure after it keeps both files and says so. A destination that exists is never
overwritten. A symlink is never moved as if it were the file it points at.

### The archive

`$ARCHIVE_ROOT/<path within its share>`, the share being `MEDIA_ROOT`'s immediate child
(§4.2.1). `ARCHIVE_ROOT` is checked by `kit/paths` before anything is read or written:
set, existing, on a different device from `/`, and writable. Free space is checked up
front, against only the bytes that cross a filesystem, per destination filesystem; a
batch that does not fit is refused before the first file moves, and a batch of renames
needs no space.

### The promotion, step by step

The seven steps of §5 are all kept, in order, and a dry run performs steps 1 to 3
(refusing exactly what the real run would) and writes nothing. Step 4 is the only
one that touches the library before the moves: it creates the destination directories and
proves each file name creatable with an empty file, which it removes again.

- **Step 2** walks every source directory with `filepath.WalkDir`; a directory that cannot
  be read fails the check rather than being skipped. `VIDEO_EXT` is `videoExts` in
  `internal/library/video.go`, a test pins it to the list in `probe-tracks.sh`, and
  extensions match in any case.
- **Step 5** also checks each file's size against the one recorded before the move,
  and that the source is gone.
- **Step 6** uses `ffprobe -of json` and the same positive-duration rule as
  before, with up to 12 probes at once, and lists every failure in manifest order. Container titles
  are then set with `mkvpropedit` and read back with `mkvmerge -J`, so a title that did not
  land is an error. Both programs go through `kit/proc`.
- **Step 7** removes only empty directories, using `rmdir`, which cannot remove a file.
  Its bound is the share the directory lives on, found as the nearest mount point; it never
  removes the share, anything above it, or the staging tree (which the rip tools require
  to exist and will not create).

**Nothing deletes media.** The only calls that remove anything are in
`internal/library/remove.go`: the source after a verified copy, a `.partial` the move
itself made, an empty probe file from step 4, and `rmdir` of an empty directory. A test
parses the package and its two commands and fails on any other call that can remove or
truncate a file or rename over one.

## 12. Manifest runs: `mkv-manifest`

`manifest-exec.py` was the most-copied script on the NAS: about thirty copies, one per
plan dir, each drifting from the others. Its gate, `manifest-precheck.py`, was a separate
program that a rushed run could skip, and the executor acted the moment it was started.
`mkv-manifest` replaces both with one tool whose executor cannot run without the gate: every
`exec`, dry or not, runs `check` first, and a single finding refuses the run before any op
is touched.

```
mkv-manifest check MANIFEST [--films]        replay every pending op on an overlay; report every finding
mkv-manifest exec MANIFEST [--go] [--films]  check, then (only with --go) perform the pending ops in order
mkv-manifest gen rename-series DIR NAME [--only N,N…] [--strip-date]   print a manifest
mkv-manifest gen archive-tree SRC DST        print a manifest
mkv-manifest --version
```

Flags may sit before or after the positionals. `check` and `exec` print their report on
stdout for exits 0 and 1 and only the `FATAL` line for 2 to 4, as the movers do.

| Exit | `check` | `exec` | `gen` |
|---|---|---|---|
| 0 | no findings | a dry run with no findings, or every pending op applied | manifest printed |
| 1 | findings | findings (nothing touched); or an op failed or the run was stopped, and a rerun resumes | n/a |
| 2 | usage; the manifest is unreadable, is not a regular file (a symlink to one is followed), is not JSON, not an object, or `ops` is missing or not an array | same | usage; DIR or SRC missing or not a directory; nothing to do |
| 3 | `ARCHIVE_ROOT` unusable while the manifest has archive ops; the journal belongs to another version of the manifest, is damaged, is a Python-era `<stem>.journal`, or is not a regular file | same, plus another `exec --go` holds this manifest, or the journal or lock file cannot be created or is not a regular file | n/a |
| 4 | internal | internal, or a journal write failure | internal |

### The manifest

```json
{"name": "penguindrum", "note": "…", "generated": {…}, "ops": [ {"kind": "rename", …}, … ]}
```

`ops` is required; `name`, `note` and `generated` (written by `gen`, never read) are
optional, and any other top-level key is a finding. Every path is **absolute and clean**.
This is deliberately unlike the movers' `MEDIA_ROOT`-relative paths: the real manifests and
the skill text have always written absolute ones, and about ninety-five of them exist.
Sources, ordinary destinations and mkvpropedit targets must lie strictly within
`MEDIA_ROOT`; archive destinations (`archive_file.dst`, `archive_dir.dst`,
`remux.archive_dst`) strictly within `ARCHIVE_ROOT`, which `kit/paths` checks as the movers
do, so a manifest with archive ops refuses with exit 3 when that guard fails. The Python
accepted any destination, the overlay included.

Every op may carry `why` and `note`, which are ignored. Any other unknown key is a finding:
the Python silently ignored `audio_nondefault`, `extra` and `after_dir_rename`, and a typo
like `audio_defualt` simply did nothing.

| `kind` | Required | Optional | Notes |
|---|---|---|---|
| `rename` | `src`, `dst` | `title`, `size` | file move (a rename, else the library's safe copy); `title` is set and read back after |
| `rename_dir`, `move_dir` | `src`, `dst` | none | directory move; the two are synonyms kept for the existing manifests |
| `archive_file` | `src`, `dst` | `size` | `dst` under `ARCHIVE_ROOT` |
| `archive_dir` | `src`, `dst` | none | `dst` under `ARCHIVE_ROOT` |
| `remux` | `src`, `dst`, `archive_dst`, `title` | `size` | mkvmerge stream copy to `dst` (which must end `.mkv`), the original archived to `archive_dst` |
| `title` | `path`, `title` | none | segment title |
| `flags` | `path`, `audio_default`, `sub_default` | none | absolute ffprobe stream indexes; `sub_default` must be present and may be `null`, meaning no default subtitle. `names` is refused: track names are never changed |
| `audio_lang` | `path`, `langs` | none | `{"<abs index>": "<ISO 639-2, three lowercase letters>"}`, non-empty; `names` is refused |
| `rmdir_if_empty` | `path` | none | never `MEDIA_ROOT`, a share (`MEDIA_ROOT/<x>`) or the staging tree |

`title` values are non-empty. `size` is a non-negative byte count that `check` compares
with the source as it will be at that point in the run; the generators write it. Targets of
`title`, `flags`, `audio_lang`, `rename` with a `title`, and `remux.dst` must end in
`.mkv`, `.mka` or `.mks`.

### `check`

Replays the pending ops, those not `done` in the journal, in order against an in-memory
overlay of the real tree and collects every problem rather than stopping at the first:
schema errors; a `src` that is not a regular file (a symlink is not one) or not a directory at that point in the run; a directory move whose source holds a symlink or special file (the library's tree scan refuses it too, naming it); a
`size` mismatch; an existing `dst`; two ops claiming one `dst`; a `dst` inside its own
`src`; a destination whose nearest existing ancestor is not a writable directory; an
mkvpropedit target that is not a file at that point; and a destination name that is over
255 bytes, not valid UTF-8, holds a control character, or ends in `.partial`, which is
reserved for copies in flight. The Python proved a name creatable by touching a file in the
library. That is replaced by these static name rules, so `check` writes nothing at all.
`rmdir_if_empty` on a missing or non-empty directory is a note, not a finding, because the
executor skips it.

`--films` adds the post-state audit from `manifest-simulate.py`, for series directories
(`MEDIA_ROOT/<share>/<category>/<series>`) whose `Movies/` any op touches, read through the
overlay so `_pending` sources work. In `Movies/` there is no subdirectory and no non-video
file; each video is named `<Series> - S99EXX - <Title (YYYY)>[ - partN].ext` with `<Series>`
equal to the directory name or, for a non-series directory (every video outside `Extras/`
sits under `Movies/`), `<Title (YYYY)> - S99EXX[ - partN].ext`; the title holds a `(YYYY)` year; the numbers run from 1 with no
gap, and one repeats only when every file holding it is a `partN`. The simulator's
hard-coded series list and its job-specific inventory report are not ported.

The report is one JSON object: `manifest`, `sha256`, `ops`, `kinds`, `done`, `pending`,
`in_doubt`, `sources` (per directory file counts), `bytes`, `notes`, `findings`, `films` and
`clean`. A finding or note carries the 1-based `op`, or `null` for whole-manifest and
`--films` findings, with its `kind`, `path` and `problem` or `note`.

### `exec`

`exec` parses the manifest, takes the per-manifest lock when `--go` is given, reads the
journal, and runs `check` with the journal in view. Any finding prints the report with the
check embedded, touches nothing and exits 1. Without `--go` it then prints the plan with
every pending op marked `pending` and exits 0; a dry run creates no journal, no lock file
and no directory. With `--go` it appends a `start` record and, for each pending op, asks the
tree again what `check` asked (a source that is a regular file of the recorded `size`, an
absent destination), then `begin`, the work, a read-back, `done`. The first failure appends
`failed` and stops with exit 1; no later op runs. `finish` is written when the run ends
after the last pending op and when it is stopped between ops.
`cli.Main` turns SIGINT and SIGTERM into a cancelled context. A cancel between ops ends the run
with exit 1, `error` "stopped before op N: …" and a `finish` record. A cancel during an op
cuts a copy or child short and fails that op: it is journaled `failed`, stays in doubt, and
a rerun assesses it.

Every move goes through `internal/library`, children go through `kit/proc`, and nothing in
`internal/manifest` removes or renames anything; a structural test asserts it, as the
library's does.

| `kind` | Performs | Read-back |
|---|---|---|
| `rename` | creates the destination directory, `Mover.Move`, then `SetTitle` when `title` is set | dst is a regular file of the same size, src is gone; the title via `mkvmerge -J` |
| `rename_dir`, `move_dir`, `archive_dir` | creates the parent, `Mover.MoveTree`: a rename, or across devices file by file through the safe move and then `rmdir` of the emptied source directories; a source holding a symlink or special file is refused before anything moves | dst's tree (relative path to size, regular files) equals the source's, recorded in `begin`; src is gone |
| `archive_file` | as `rename`, no title | as `rename` |
| `remux` | discards a leftover `dst.partial`; `mkvmerge -q -o dst.partial src` (exit 0 or 1); verifies; sets the title on the partial; places it at `dst` with `library.PlacePartial` (a same-directory rename, never a copy, refusing an existing `dst`); moves `src` to `archive_dst`. A failure up to and including the placing discards the partial | decoded frame count equal and not zero (`ffprobe -count_frames -of json`, v:0; unreadable fails); durations within 0.1 s; playable stream signature (type, codec, channels, width, height) equal; ffmpeg null-decode error lines of `dst` no more than `src`'s; non-media source streams reported in `detail` |
| `title` | `SetTitle` | `mkvmerge -J` |
| `flags` | `SetDefaultFlags` | every audio and sub disposition as wanted, exactly one default audio, at most one default sub, every track name unchanged |
| `audio_lang` | `SetAudioLanguages`, including the Matroska `LANGUAGE` tag rewrite the Python did, because Plex reads the tag over the Language element | every audio stream's language as wanted, titles and defaults unchanged; the rewritten tags as wanted |
| `rmdir_if_empty` | `RemoveDirIfEmpty` | a missing or non-empty directory is `skipped` with a detail, not a failure |

mkvpropedit addresses tracks as `track:aN` and `track:sN`. The absolute ffprobe index maps
to N by order among tracks of that type, and the op refuses when ffprobe's and
`mkvmerge -J`'s per-type counts differ rather than guess.

The report adds `dry_run`, the embedded `check`, `ops`, `already_done`, `applied`, a
`results` entry per pending op (`op`, `kind`, `target`, `status` of `done`, `settled`,
`skipped`, `pending` or `failed`, `bytes`, `detail`), `bytes`, the `journal` path, `error`
when the run stopped early and `failed_op` when an op itself failed.

### Journal, lock and resume

The journal is `<manifest dir>/<manifest name without .json>.journal.jsonl`, append-only,
one JSON line per record, each fsynced: `start` (manifest SHA-256, op count, `at`, tool
version, pid), `begin` (op, kind, `at`, the state settling needs: the source size or its
tree), `done` (op, `at`, bytes, detail), `failed` (op, `at`, error) and `finish` (`at`, ops
applied in the run). Because a manifest sits in its job's plan dir on the NAS, so does its
journal, and a pod roll loses nothing.

Every `start` must carry the current manifest's SHA-256. A manifest edited after ops were
applied is exit 3 (the message counts every op journaled `done`), as is a Python-era
`<stem>.journal` beside it: resuming that by index would redo ops. A line cut short by a
crash is accepted only as the last line, or when a `repair` record follows it; any other
unparseable line, an unknown event or an op outside the manifest is exit 3. A run that
opens a journal whose last line was torn ends that line with a newline and appends
`repair` (`at`, `torn_bytes`) before anything else, because nothing in a journal is ever
truncated. The journal, like the lock file, must be a regular file; it is opened with
`O_NOFOLLOW`, and one that is a symlink, FIFO, device or directory is exit 3.

`exec --go` holds `flock(LOCK_EX|LOCK_NB)` on `/tmp/mkv-manifest-<first 16 hex of
sha256(absolute manifest path)>.lock`, pod-local like the drive lease, so two execs of one
manifest cannot interleave; the second gets exit 3. The lock is taken before the journal
is read and `check` runs, and the lock file is never removed.
Because the lock is per pod, `mkv-manifest` is run in the library pod
(`deploy/mediaops`) only; the ripper mounts only the staging tree.

On a rerun, ops with `done` are skipped. An op with `begin` and no `done` is in doubt and is
assessed from the tree before anything runs. `check` does the same, so the gate and the
executor see one picture:

| `kind` | src gone, dst whole | src whole, dst absent | both present |
|---|---|---|---|
| file moves | `settled`, journaled `done`; a `title` is still applied | redone | conflict: a finding naming both paths, never resolved by deleting |
| directory moves | `settled` when dst's tree equals the recorded one | redone | resumed: the remaining files move; a file present on both sides is a conflict |
| `remux` | `settled` when `src` is gone and `dst` and `archive_dst` exist | redone, the partial discarded | `dst` and `src` present, `archive_dst` absent: re-verify, title, archive. Any other mix is a conflict |
| `title`, `flags`, `audio_lang`, `rmdir_if_empty` | redone, being idempotent | redone | redone |

### The generators

Both print a manifest with `"generated": {"by": "mkv-manifest gen …", "args": […], "at": …}`
and write nothing; review the output, then `exec` it.

- `gen rename-series DIR NAME` turns each `Season N` directory (all, or `--only`) into
  `rename` ops with a `size`, to `Season 0N/NAME - SxxExx<rest>` for every file that
  matches the `rename-series.py` episode pattern; `--strip-date` drops a trailing
  ` - [YYYY-MM-DD]`. When the folder is being padded, its other files move unchanged by
  `rename`, its subdirectories by `move_dir`, and an `rmdir_if_empty` of the old folder
  follows. Files left in an already-padded folder are listed in `generated.unmatched`. No
  `Season` directory, or no episode at all, is exit 2.
- `gen archive-tree SRC DST` writes one `archive_file` (with `size`) per regular file under
  SRC to the same relative path under DST, then `rmdir_if_empty` for each directory of SRC,
  deepest first and SRC last. DST may already exist, though not inside SRC, and only per-file
  collisions are findings, as in `archive-tree.py`. A symlink or special file under SRC, or an empty SRC,
  is exit 2.

### What changed from the Python

- Dry run is the default and `--go` acts. The Python acted at once.
- `check` is part of every `exec`, so it cannot be skipped.
- Unknown keys are findings; `names` on `flags` and `audio_lang` is refused.
- Archive destinations must lie under `ARCHIVE_ROOT`.
- Probes use `-of json`. The Python's `frames_and_packets` used `csv=p=0`, the exact
  pattern the skill warns against.
- Directory moves cross devices. `manifest-exec.py`'s `move_dir` could not, which is why
  Outlanders' extras move was a hand-written `run.sh`.
- `sub_default` is required but may be `null`, so "no default subtitle" is stated rather
  than implied by an absent key.
- `defer` is not ported; no real manifest uses it.
- The touch test is replaced by the static name rules above.
- Progress is journaled beside the manifest and resumable, where the Python kept a bare
  index in `<stem>.journal`.

### Tests

`internal/manifest/testdata/` holds three manifests from real plan dirs, with their
share names rewritten to neutral ones and a `README` recording every transformation:
Penguindrum's `rename-manifest.json`, a coherent subset of `film-plan-v2.json` covering
`remux` and the directory and archive ops, and Outlanders, whose hand-run script is
translated into ops. Tests build each fixture's tree in a temp dir and run `check` and
`exec --go` against it with a fake `proc.Runner` that keeps each file's metadata in the
file's own bytes, so moves carry it. A test tagged `integration` runs `remux`, `title`,
`flags` and `audio_lang` against real mkvtoolnix and ffmpeg.

## 13. Play-All analysis, splitting and audits

`mkv-probe`, `mkv-bounds`, `mkv-split`, `mkv-order`, `mkv-anilist` and `mkv-audit` are the
tools between a rip and a manifest: they read the tracks, find the episodes in a Play-All,
cut them out and prove the cut, and audit the library before and after a manifest run. They
replace `mkv-probe`, `mkv-bounds`, `mkv-split`, `mkv-order` and `mkv-anilist` from `scripts/`
and the `audit-series`, `audit-films`, `blindspot`, `film-plan-sweep` and per-job
`postcheck` scripts that lived in plan dirs. They are library tools: run them in the
workbench pod (`deploy/mediaops`), not the ripper, and they carry no role guard.

```
mkv-probe PATH... [--workers N]                         inventory: one JSON object
mkv-bounds FILE EPISODE_MINUTES                         episode boundaries of a Play-All
mkv-split SRC OUTDIR/PREFIX CH,CH,... [--go] [--tol S] [--allow-open-gop]
mkv-split SRC OUTDIR/PREFIX --at T,T,... [--go] [--tol S] [--allow-open-gop]
mkv-split verify SRC PART PART...                       prove a split; reads only
mkv-order INVENTORY.json [DIR] [--min-episode-minutes 15] [--max-episode-minutes 40]
mkv-anilist SEARCH | mkv-anilist --id N
mkv-audit series DIR... | --list FILE  [--workers N]
mkv-audit films [ROOT...] [--min-minutes 60]
mkv-audit blindspot [ROOT...]
mkv-audit sweep MANIFEST [ROOT...] [--min-minutes 60]
mkv-audit postcheck MANIFEST --before INVENTORY.json [--workers N]
```

Each prints one JSON value on stdout for exits 0 and 1 and only the `FATAL` line for 2 to 4.
Flags may sit before or after the positionals. Only regular files are probed or read: a
FIFO or device under a walked tree is skipped and named, never opened. ffprobe is only run
with `-of json`, and every child goes through `kit/proc`, whose guard still refuses anything
that would re-encode.

`ROOT` defaults to the pipeline share's `anime` and `ecchi` categories. A `DIR` or `ROOT`
that does not exist or is not a directory is exit 2 naming it.

| Exit | probe | bounds | split (dry run / `--go`) | split verify | order | anilist | audit |
|---|---|---|---|---|---|---|---|
| 0 | every file probed | a pattern | no findings / split placed and verify clean | clean | every disc `confirmed` | entry printed | clean |
| 1 | a file failed to probe (its record carries `error`) | no pattern, or no chapters | precheck findings, nothing written / part count wrong or verify findings, parts kept | findings | a disc is `mismatch`, `no_playall` or `unreadable` | n/a | findings |
| 2 | usage; PATH missing | usage; hint not positive; FILE missing or not regular | usage; SRC not regular; OUTDIR is a file; `%` in OUTDIR/PREFIX; no chapters; chapter out of range; cuts not increasing or outside (0, duration) | usage; SRC given with no parts | usage; inventory unreadable or not an `mkv-probe` document; no record under DIR | usage; AniList says Not Found | usage; missing root, dir, list, manifest or inventory |
| 3 | n/a | n/a | an output `PREFIX-NNN.mkv` exists | n/a | n/a | n/a | the journal is unusable, as for `mkv-manifest` |
| 4 | internal | ffprobe failed | ffprobe, mkvmerge or ffmpeg failed, I/O | ffprobe or ffmpeg failed | internal | network, HTTP status, rate limit, bad JSON | internal |

### `mkv-probe`

PATH is a file or a directory. A directory is walked, sorted and without following symlinks,
for every video (`library.IsVideo`, not only `.mkv`): a library that holds `.ogm` and `.avi`
episodes is invisible to a probe that filters on `.mkv`. `--workers` defaults to 12.

```json
{"roots": ["/abs"], "probed": 41, "errors": 0, "skipped": ["/abs/fifo"],
 "files": [{"path": "/abs/x.mkv", "rel": "share/anime/X/…", "size": 39094659279,
            "duration": 8550.046, "title": "Fate/Apocrypha",
            "chapters": [0, 335.669, …],
            "streams": [{"index": 0, "type": "video", "codec": "h264", "lang": "eng", "title": null,
                         "default": true, "channels": 0, "width": 1920, "height": 1080}]},
           {"path": "/abs/bad.mkv", "error": "ffprobe exited 1 …"}]}
```

Durations and chapter starts are seconds. `rel` is relative to `MEDIA_ROOT` and `null` outside
it. Records are sorted by path. A file ffprobe cannot read, or whose duration is not a
positive number, becomes a record with only `path` and `error` and the run goes on; any such
record makes the exit 1, so a partial inventory is never mistaken for a whole one.
`internal/inventory` owns the types and `Read`, which `mkv-order` and `mkv-audit postcheck`
use: a document without a `files` array, or with a record without a `path`, is not an
inventory.

### `mkv-bounds`

The detector finds the episode length that best fits a Play-All's chapter starts: a greedy
walk of chapters from the start, a scan of candidate lengths in 0.05-minute steps within
0.82 to 1.22 times the hint, a 1.5-minute tolerance on each boundary, a 3-minute lead-in, and a
last episode between 0.55 and 1.9 times the episode length. Candidates are ordered by most
episodes, then smallest fit error, then earliest boundaries. The hint is required: without one a
Play-All of 24-minute episodes also fits as twice as many half-episodes (Sailor Moon S01 D01,
40 chapters: 8 episodes at hint 24, 16 without).

```json
{"file": "/abs", "duration": 8550.046, "chapters": 26, "hint_minutes": 24,
 "pattern": true, "episodes": 6, "episode_minutes": 23.7503, "fit_error_minutes": 0.0203,
 "last_minutes": 23.7495, "grade": "auto", "boundaries": [0, 1425.0, …],
 "split_chapters": [6, 10, 14, 18, 23], "split": "chapters:6,10,14,18,23",
 "limit": "A constant offset is invisible to every statistic here: …"}
```

Durations and boundaries are seconds; `episode_minutes`, `fit_error_minutes` and
`last_minutes` are the detector's own minutes. `split_chapters` are 1-based chapter numbers,
the form `mkv-split` takes, and `split` is the same list as `chapters:N,M`. `grade` is `auto`
when the fit error is at most 0.35 minutes and the episode length is within 12 % of the hint,
and `review` otherwise. No pattern prints `"pattern": false` and a `reason`, and exits 1.

**The limit.** A set of cuts shifted by a constant offset has the same stride, spread, fit error
and episode length as the correct set, so no figure here can say the anchor is right (§6). The
fixture is Demon Slayer S02 D02 (22 chapters): at hint 24 the detector grades `auto` at fit error
0.226 with `chapters:7,14,20`, while the library's own S02E04 to S02E07 durations put the true cuts
at chapters 7, 13 and 19, so the second cut lands 1.57 minutes inside E06. The test documents
that limit rather than a pass, and every pattern carries it in `limit`. One interior boundary per
track is looked at with `mkv-frames` before the track is split.

### `mkv-split`

**Plan and precheck**, on a dry run and on `--go` alike, reading only. Chapter mode takes
1-based chapter numbers in `[2, chapters]`, strictly increasing; a cut is at the chapter's
start. `--at` takes seconds, strictly increasing and inside `(0, duration)`. For each cut time
`t`, ffprobe reads the first video stream's packets over `[t-2, t+4]`, and:

- **Keyframe, chapter mode.** mkvmerge is handed the chapter numbers and starts each part at
  the first keyframe at or after the chapter's start, so that is where the cut lands: the first
  video keyframe with pts at or after `t - 0.001` (the Matroska timestamp scale is a
  millisecond), which must be within `--tol` after `t` (default 0.042 s, one frame at 24 fps). A
  keyframe a few milliseconds *before* the chapter start would put the cut a whole GOP late, so
  it is a finding.
- **Keyframe, `--at`.** The cut lands on the keyframe nearest `t` on either side, within
  `--tol` (the earlier on a tie), the acceptance `mkv-split-ts.py` had. mkvmerge is handed that
  keyframe's own timestamp, built from its integer pts so that no rounding can move it. The
  Python handed mkvmerge `t`, so a keyframe a few milliseconds before `t` cut a whole GOP late.
- A cut that misses is a finding, `not a keyframe`, naming the nearest keyframe on each side.
- **Open GOP.** Any packet after the keyframe, before the next keyframe, whose pts is lower than
  the keyframe's is a leading picture: the GOP is open and those frames would be lost or
  undecodable at the start of the part. It is a finding (`open GOP`), turned into a note by
  `--allow-open-gop`, which records the owner's decision. The splitting reference's rule
  ("if any boundary has them, the split is not clean") is now a gate.

An output `PREFIX-NNN.mkv` already present is exit 3. A dry run then prints the report and exits
0, or 1 with findings, and creates nothing, not even OUTDIR.

**`--go`.** Create OUTDIR; discard leftover `PREFIX-NNN.mkv.partial` files (only names ending in
`.partial` are ever removed); run `mkvmerge -o PREFIX-%03d.mkv.partial --split chapters:…|timestamps:…
SRC` (exit 0 and 1 are accepted, 2 or more is exit 4 with mkvmerge's tail); require exactly
`cuts + 1` partials, or a finding with the partials left in place and exit 1; place each as
`PREFIX-NNN.mkv` without overwriting (an existing file is exit 3); then run `verify` with the
planned keyframe times and embed its report. The source is never written, renamed or removed,
and no part is removed on any failure. A split is not done until its verify is clean.

**`mkv-split verify SRC PART...`** takes the parts in order and reads only. Each row is a
finding when it fails:

| # | Check |
|---|---|
| V1 | at least two parts; each a regular file ffprobe can read |
| V2 | the stream layout of each part (index, type, codec, channels, width, height, language, title, default) equals the source's |
| V3 | **packet accounting, every stream:** the parts' packets laid end to end equal the source's packets one for one, in order, none dropped and none duplicated. A video stream in AVCC form (h264 or hevc with a NAL length size) compares the MD5 of each payload with every access unit delimiter NAL removed on both sides, because mkvmerge drops those and changes nothing else; a PCM stream is compared as bytes, not packets, because mkvmerge re-frames PCM even on a plain remux: the source's raw sample bytes must equal the parts' laid end to end, by count and MD5 (`compared: bytes` in the report); any other stream compares the payload MD5 of a stream copy. The finding names the stream, part and packet, or the byte totals |
| V4 | **timing:** per part, the shift is the source's minimum video pts over the part's span minus the part's minimum video pts; every packet of every packet-compared stream lands within 2 ms of its source pts after that shift, and each part's first PCM packet within 2 ms of the sample clock its byte offset gives |
| V5 | each part's first video packet is a keyframe; its leading pictures are counted, as a note |
| V6 | a full decode of each part: decoded video frames equal the part's video packets minus its leading pictures, with no decoder error except the known open-GOP notices on a part that has leading pictures |
| V7 | **seams:** at each cut, the source is decoded over `[cut-6 s, cut+3 s]` continuously; the outgoing part's last decoded frames and the incoming part's first are aligned to it by MD5, and the frames between them are the ones lost. A finding when either side cannot be aligned uniquely, when they overlap, or when any lost frame differs from the outgoing part's last frame (a held frame is invisible; a lost picture is not) |
| V8 | the parts total 99.0 to 100.5 % of the source's bytes (one set of headers becomes N, and the Play-All's chapter table is dropped) |
| V9 | from `mkv-split --go` only: each actual cut equals its planned keyframe within 1 ms |

V3 replaces the Python's per-part duration printout. Container duration is reported and never
judged: mkvmerge zeroes each part on its earliest presentation time, and a subtitle or PCM frame
straddling the cut moves it. Packet hashes of raw payloads never match across a split, and that
was read as "packet comparison is impossible"; with the delimiters removed exactly they do.

```json
{"source": "/abs", "bytes": 1, "duration": 1.0,
 "parts": [{"path": "/abs/p-001.mkv", "bytes": 1, "duration": 1.0, "cut": null,
            "first_keyframe": true, "leading_pictures": 0, "decoded_frames": 1, "decode_errors": []}],
 "streams": [{"index": 0, "type": "video", "codec": "h264", "packets": 1, "accounted": 1,
              "identical": true, "aud_removed": 1, "max_pts_deviation_ms": 0}],
 "seams": [{"after_part": 1, "cut": 1425.05, "lost_frames": 0, "lost_identical": true}],
 "bytes_percent": 99.947, "findings": [{"part": 2, "stream": 0, "problem": "…"}],
 "notes": [], "clean": true}
```

The split report is `{"source", "bytes", "duration", "chapters", "mode": "chapters|timestamps",
"cuts": [{"n", "chapter"?, "requested", "keyframe", "delta_ms", "leading_pictures"}], "outputs",
"dry_run", "findings": [{"cut"?, "problem"}], "notes", "mkvmerge"?: {"argv", "exit"}, "verify"?:
{…}, "clean"}`. `clean` on a `--go` is true only when the plan had no findings and `verify` is clean.

### `mkv-order`

Reads `mkv-probe`'s object. Its records under DIR (default: all of them) are grouped by parent
directory, one disc each, sorted by path. Per disc, the Play-All is the longest track and the
episodes are the others within `[--min-episode-minutes, --max-episode-minutes]`. A longest track
shorter than the maximum, or no episodes, is `no_playall`; a disc with an error record is
`unreadable`. Otherwise the episodes are laid end to end in track order, and each episode's own
chapter marks, shifted by the time before it, and each boundary between episodes must match the
Play-All's next chapter marks within 3 seconds; the disc is `confirmed` or `mismatch` with the
first place it failed.

```json
{"inventory": "/abs", "dir": "/abs", "confirmed": 2, "total": 3,
 "discs": [{"dir": "/abs/Disc 01", "status": "confirmed|mismatch|no_playall|unreadable",
            "playall": "/abs/…_t00.mkv", "episodes": ["…"], "detail": "8 episodes, 40/40 chapters consumed",
            "episode_minutes_sum": 192.9, "playall_minutes": 192.99}]}
```

**Gap closed:** the Python selected the series by the third component of the record's `rel`,
which named the category and never the series once the box segment joined `MEDIA_ROOT`, so it
silently printed nothing. Discs are now selected by the record's absolute path, and a DIR with
no record under it is exit 2. A confirmed disc says the tracks are sequential; it never says
which episode number the first one is.

### `mkv-anilist`

`POST https://graphql.anilist.co` with the Python's query, a `User-Agent` of `mediaops/<version>
(library normalisation)` (AniList answers 403 without one), a 30-second timeout, and the response
read through an 8 MiB limit. `--id N` sends only `{"id": N}` and a search only `{"s": "…"}`: the
`id` variable is omitted rather than sent as `null`, because an explicit null makes AniList answer
404 for a search that is good. A `Not Found` error is exit 2; any other GraphQL error, HTTP status,
network failure or unreadable body is exit 4.

```json
{"id": 10721, "title": {"romaji": "…", "english": "…", "native": "…"}, "episodes": 24,
 "format": "TV", "year": 2011, "streaming_episodes": ["…"],
 "note": "streamingEpisodes is ordered but not numbered; treat its order as advisory"}
```

`episodes`, `format`, `year` and each title may be `null`; so may a streaming episode's title.

### `mkv-audit`

Read-only; the only child is ffprobe. A finding is `{"code", "path", "detail"}` plus `scope`
(`content` or `extras`, where a rule treats them apart) or `op` (the 1-based manifest position)
when it has one. Any finding is exit 1. `--workers` defaults to 8.

- **`series`** checks each series directory against the promoted-series rules: `EMPTY`, `LEGACY`
  (non-Matroska video, to which the track rules do not apply), `UNREADABLE`, `TITLE`,
  `AUDIO-default-count`, `AUDIO-not-jpn`, `SUBS-default-count`, `LOOSE`, `SHAPE`, `PREFIX`, `NUM`
  (missing, duplicate) and `LAYOUT`. Episodes are enumerated by video, not by `.mkv`. Output is
  `{"series": [{"dir", "name", "files", "findings", "clean"}], "clean": n, "total": n}`; a
  series that holds a FIFO or other special file names it in `skipped`. `--list FILE` reads
  one directory per line.
- **`films`** finds entries that break the `Movies/` convention: `YEAR-IN-DIR`, `BARE-FILM` (a
  loose video of at least `--min-minutes` in a series root with no `Movies/`), `UNREADABLE`,
  `NO-S99` and `S99-MISPLACED`. Output is `{"roots", "findings", "counts", "clean"}`.
- **`blindspot`** lists every series with video an `.mkv` filter hides: per series the mkv
  count, the other-video count by extension and the loose files in its root, as a `BLINDSPOT`
  finding. Counts come from walking the tree, never from a manifest.
- **`sweep MANIFEST`** reads an `mkv-manifest` manifest and checks it against the library before
  it runs: file ops' `src` are the plan's sources, and `rename_dir` and `move_dir` its directory
  moves. Coverage (`UNCOVERED-YEAR-IN-DIR`, `UNCOVERED-S99`, `UNCOVERED-BARE-FILM`), a `Movies/`
  subdirectory left holding files the plan does not move (`STRAY-SUBDIR`), container titles of
  films the plan does not touch (`TITLE-EMPTY`, `TITLE-DIFFERS` against the filename's title), and
  a directory the plan empties with nothing to remove it (`RESIDUE`). Schema problems in the
  manifest are `SCHEMA` findings and the sweep runs on what was readable. The post-state shape,
  prefix, year, number and gap pass is `mkv-manifest check --films` and is not repeated: the report
  says so in a note.
- **`postcheck MANIFEST --before INVENTORY.json`** reads the tree a manifest run left behind and
  reports every way it differs from the manifest, using its journal. `--before` is an
  `mkv-probe` inventory taken before the run and is required. It finds ops not journaled done
  (`NOT-DONE`); a file op's `src` still present or `dst` not a regular file (`SRC-REMAINS`,
  `DST-MISSING`, `ARCHIVE-MISSING`); a directory op's `dst` missing; titles, default flags and
  languages that do not read back (`TITLE-MISMATCH`, `FLAGS-MISMATCH`, `LANG-MISMATCH`); an
  `.mkv` destination whose `ffprobe -v error` is not silent or whose duration is not positive
  (`INTEGRITY`, `UNREADABLE`); a track layout other than the `--before` record of the op's `src`,
  minus default flags (`TRACKS-CHANGED`; a missing record is `NO-BEFORE`, because a check that
  cannot run does not pass); a `Season NN` directory whose files do not share one full track
  signature (`SEASON-SIGNATURE`); and the `series` audit over every series directory a
  destination lies in. Size changes are notes (`SIZE-CHANGED`), because an in-place title edit
  may grow a file. Output is `{"manifest", "sha256", "ops", "done", "findings", "notes",
  "series", "clean"}`. It does not render the identity card sheet; post-rename identity stays
  its own step.

**Gaps closed.** A missing series or root is exit 2, where `audit-series` printed `EMPTY` for
it, exactly like a series with nothing in it. An unreadable duration is `UNREADABLE`, where
`audit-films` read it as 0 minutes and passed it. `sweep` reads the manifests `mkv-manifest`
runs, not the older `rows` and `dir_renames` plan. `postcheck` is one tool with the
`--before` comparison built in, instead of four per-job scripts that drifted.

### What changed from the Python

- `mkv-probe` writes one JSON object, not lines, in seconds, not rounded minutes; walks every
  video extension; skips and names what is not a regular file; fails closed on a bad duration.
- `mkv-bounds` is the same detector, ported exactly, with the limit and the split chapters in
  its output.
- `mkv-split` prechecks keyframes and open GOPs before writing anything, and passes mkvmerge the
  keyframe's own time for `--at`. The Python verified part count, per-part duration and total
  bytes; `verify` accounts for every packet, checks timing, decodes each part, compares the seams
  and holds the byte total to 99.0 to 100.5 %. Outputs are `.partial` until placed, and nothing is
  overwritten. `mkv-split-ts.py` is `--at`.
- `mkv-order` selects discs by directory, and a DIR that matches nothing is an error.
- `mkv-anilist` prints JSON instead of text, takes `--id N` instead of a second positional, and exits 2 for a series AniList does not know.
- `mkv-audit` replaces the per-plan audit, sweep and postcheck scripts.
- A missing directory is exit 2 in every tool, not an empty result.

### Tests

`internal/bounds/testdata/` holds `ffprobe -show_format -show_streams -show_chapters -of json`
output for three Play-Alls, read over NFS for metadata only with the share path rewritten, and a
`README` recording each source: Fate/Apocrypha S01 D01 (26 chapters, 6 episodes,
`chapters:6,10,14,18,23`, `auto` at hint 24), Sailor Moon S01 D01 (the half-episode trap) and Demon
Slayer S02 D02 (the offset limit above). `internal/split` builds synthetic MKVs per test from lavfi
sources (H.264 with access unit delimiters, PCM, SRT), splits them with mkvmerge, and checks that a
clean split passes while a dropped packet, a part cut on a non-keyframe and a short part each fail,
that an `--at` a few milliseconds off a keyframe lands exactly on it, and that in chapter mode a keyframe a few milliseconds before the chapter start is a finding. These tests need ffmpeg and
mkvtoolnix and fail without them; CI's `test` job installs both and also runs the
`integration`-tagged tests. `internal/anilist/testdata/` holds recorded responses (a search, an id
lookup, Not Found), replayed from an `httptest` server.

## 14. The Go toolchain as the ad-hoc runtime

The image carries the Go toolchain so one-off programs run in the pod, where every media
write has to happen, the way a throwaway `.py` script did. `GOROOT` is `/usr/local/go`,
copied from the build stage so the compiler that runs one-offs is the one that built the
tools, and trimmed of `test/`, `api/`, `doc/`, `misc/` and `src/**/testdata`. It is its own
layer, ahead of everything that changes per build.

The module's source is at `/opt/mediaops/src` (root-owned, read-only to `media`), and a job
template at `kit/template` carries a `go.mod` with a `replace` onto that path. Dependencies
come from a read-only module cache at `/opt/mediaops/gomod`, filled by `go mod download` in
the build stage, so it holds exactly the module's dependencies. The environment is
`GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod CGO_ENABLED=0`,
`GOMODCACHE=/opt/mediaops/gomod` and `GOCACHE=/work/.cache/go-build` (the `/work`
emptyDir). With `GOPROXY=off` anything outside the standard library, `kit` and
`golang.org/x/sys` fails at compile time instead of fetching.

```bash
# PLAN is the plan dir's pod path, set on the workstation; the double quotes expand it
# before the command reaches the pod, which has no PLAN of its own.
kubectl -n mediaops exec deploy/mediaops -- bash -c \
  "cp -r /opt/mediaops/src/kit/template '$PLAN/src/seamcheck' && cd '$PLAN/src/seamcheck' && go mod tidy && go run . ARGS"
```

The build proves this offline: a `RUN` copies the template, runs `go mod tidy` and
`go build` with no network, and fails the image if the template does not resolve `kit` and
`x/sys`.

`go run` reports any non-zero exit of the program as its own exit 1, followed by an
`exit status N` line on stderr. A one-off whose exit code carries meaning is built with
`go build -o /tmp/<name> .` and run directly.

Go replaced Python as the pod's ad-hoc runtime outright: the image has no `python3`, and a
`RUN ! command -v python3` fails the build if a package dependency brings it back. The
`.py` files under `_pending/anime/_plans/` are history and no longer run.

## 15. Frame evidence: sheets, fingerprints and title cards

`mkv-frames`, `mkv-titlescan`, `mkv-fingerprint`, `mkv-dupcheck`, `mkv-staticcheck` and
`mkv-cards` replace the last frame-evidence scripts in `scripts/` and the skill's
`cardfind.py` and `cardfreeze.py`, along with the per-job `cards.py`, `chapsheet.py` and
`seamsheet.py` that plan dirs kept rewriting. They are library tools for the workbench pod.

```
mkv-frames FILE OUT.png T... [--width 640] [--cols N]
mkv-titlescan FILE OUT.png START END [--step 3] [--band full|H|H:Y] [--width 340]
mkv-fingerprint hash FILE PREVIEW_START|none OUT.json
mkv-fingerprint batch JOBS.tsv OUTDIR [--workers 6]
mkv-fingerprint match OUTDIR
mkv-dupcheck OUTDIR TRACK
mkv-staticcheck FILE... [--workers 4]
mkv-cards find FILE... [--lo 60] [--hi 200] [--miny/--maxy/--minu/--maxu/--minv/--maxv N]
                       [--minrun 3] [--static] [--tol 0.01] [--workers 4]
mkv-cards freeze FILE... [--lo 92] [--hi 520] [--db -30] [--mindur 3.5] [--minv 150]
                         [--maxv 255] [--minu 0] [--maxu 122] [--minymax 0] [--workers 4]
mkv-cards sheet OUT.png --candidates REPORT.json|- | --points POINTS.tsv|- |
                        --chapters FILE [--offset 3] | --seams PART PART...
                        [--width W] [--cols N] [--subs N]
```

Times are seconds or `[HH:]MM:SS[.frac]`. Each prints one JSON value on stdout for exits 0
and 1 and only the `FATAL` line for 2 to 4. Exit 1 means a tile had no frame (a placeholder
is drawn and the sheet is still written), a fingerprint job failed, `match` has findings, a
file was unreadable, or `find`/`freeze` found no candidate in some file. `none` is not a
pass.

**What they write.** Only the `OUT.png`, `OUT.json` or `OUTDIR` you name. A sheet must end in
`.png`, a fingerprint in `.json`, so no output can be a media file. An existing regular file is
replaced atomically at mode 0660, and anything else at that path is refused. There is no
`--go`, because nothing outside the named output is touched.

**Sheets.** ffmpeg decodes each tile to raw RGB through the never-re-encode guard, with the
display aspect restored, the band cropped, and the frame fitted into a fixed box. Go composes
the sheet, draws the labels with an embedded 5×7 font and encodes the PNG. Labels no longer
depend on fontconfig (§9's defect), and one decode can label every frame differently.
`titlescan` decodes its window once. A sheet holds at most 600 tiles and 100 Mpx.

**Fingerprints.** The dHash is the Python's bit for bit: 9×8 gray, a bit per left-greater-than-
right pair, the same ffmpeg argv. `internal/dhash/testdata` holds the Python's own output on
recorded frames, including real ones from Berserk and Cutie Honey. Records are JSON with hex
hashes. `batch` writes each record whole, so a killed batch resumes and ends byte-identical to
an uninterrupted one. It skips a record only when it matches the job's path and preview
start, and it never overwrites one that does not. `match` ranks the way the Python did and
calls a link `confident` at match ≥ 0.30 and gap ≥ 0.15. A weak link, or two tracks claiming
one successor, is a finding.

**Title cards.** `find` keeps every 1 fps run that holds the card's colour; `freeze` keeps
every `freezedetect` span whose colour fits. Both report every candidate with its figures,
plus the most saturated as `best`. `sheet --candidates` tiles them all. The measured
signatures are tests:

| Show | Method | Flags |
|---|---|---|
| Berserk (red on black, held byte-identical) | find | `--maxy 25 --minv 127 --static` |
| Cutie Honey (white on red, after red scenery) | find | `--lo 90 --hi 420 --minv 160 --maxu 115`; the most saturated run, not the first |
| Cutie Honey (card breathes under its fade) | find | `--static --tol 0.5`; 0.01 misses it |
| Cutie Honey | freeze | defaults (−30 dB; −55 dB finds nothing on its grain), `--minymax` for white glyphs |
| Cutie Honey E04 (dark blue) | limit | no colour method finds it: read a sheet |

### What changed from the Python

- Every ffmpeg exit status is checked. The Python fingerprint and staticcheck read a dead
  decode as a track with no frames.
- A `T` past the end of a file is a placeholder tile and exit 1. It is no longer silently
  dropped from the sheet.
- `freeze` notes a freeze still running at the window's end, which the Python dropped.
  Freezes rejected on colour are listed with their figures.
- Fingerprints are written at 0660. The Python ran without umask 0007.
- `PREVIEW_START` of 0 is an error. The Python read it as `none`.
- `titlescan`'s STEP and BAND, and `batch`'s workers, are flags.
- Python-era fingerprint directories are not read.
