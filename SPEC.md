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
| `imagemagick` + `font-dejavu` | `montage` for timestamped contact sheets. The font is not optional — `-annotate` silently produces unlabelled tiles without it, and an unlabelled contact sheet cannot be used to verify a cut point |
| `python3`, `ca-certificates` | the detection/promotion scripts; AniList GraphQL over TLS (stdlib `urllib` is sufficient — no `requests`) |
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

### Baked-in scripts → `/opt/mediaops/bin` (on `PATH`)

Versioned in the image repo under `scripts/`, except `mkv-drives` and `mkv-rip`,
which are Go programs under `cmd/` (shared code in `kit/` and `internal/`) built
into the image by a `golang` stage; the rest follow the same way, a tool at a time.
These were developed against the backlog and are the reason this is an image and
not just a package list:

| Script | Purpose |
|---|---|
| `mkv-probe` | bulk inventory → JSON: duration, chapter timecodes, stream layout, per-track |
| `mkv-bounds` | episode-boundary detection over a Play-All's chapter list; emits the `mkvmerge --split chapters:N,M` argument |
| `mkv-frames` | render N timestamped frames from a file, tiled into one contact sheet |
| `mkv-split` | drive `mkvmerge --split`, then verify part count, per-part duration and total-bytes-vs-source |
| `mkv-promote` | manifest-driven mover with full accounting (see §5) |
| `mkv-archive` | archive-before-promote into `$ARCHIVE_ROOT/<path within its share>`, preserving the layout the file had |
| `mkv-drives` | Go. One JSON array of the optical drives: serial, device nodes, USB port, link speed, tray/media state (§10) |
| `mkv-rip` | Go. Staggered, health-gated disc ripping into `_pending` with verification and a manifest; `identify`, `finalize`, `stop` (§10) |

**Split of responsibility:** stable tooling is baked into the image; *working
state* — the split manifest, the content-verified corrections, run logs — lives
on the NAS under `_pending/anime/_plans/`, so iterating on a plan never
requires an image rebuild. In-flight edits to a script can be run from the NAS
copy and promoted into the image once settled.

> This split is also a direct response to losing the previous session's entire
> working set: it was in `/tmp`, and `/tmp` was cleared. Nothing durable goes
> in a scratchpad again.

### CI

`.github/workflows/build.yml`, copied from `Lucebox-DFlash` and extended. A `test`
job runs on every pull request and every push to `master` touching the image's
inputs (`Dockerfile`, `scripts/**`, `go.mod`, `go.sum`, `cmd/**`, `kit/**`,
`internal/**`): no tool may exist as both `scripts/<name>` and `cmd/<name>/`, then
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

Pinned to `styxeon` by `nodeAffinity` — **it is the wired-10G node, and that is
the entire point of the app.** styxeon currently sits at 20% CPU / 22% memory
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
itself by `mkvlib.share_root()`. That made every archive move a rename, but it
also scattered the archive across every NAS and let the working box fill up with
material that is, by definition, never read again. It is now a single tree,
configured by **`ARCHIVE_ROOT`**.

The working categories stay on dxp6800 and the archive lives on dxp4800, and
that split is permanent rather than a migration in progress. **Crossing a
filesystem boundary is therefore the normal case for every archive move, not an
exceptional one** — which is exactly what the previous design refused to do.

`ARCHIVE_ROOT` has **no default**, unlike `MEDIA_ROOT`. The risk is asymmetric: a
wrong `MEDIA_ROOT` finds nothing and fails loudly, while a wrong `ARCHIVE_ROOT`
accepts real data onto the overlay and reports success. `mkvlib.archive_root()`
refuses to proceed unless the variable is set, the directory already exists, its
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

`share_root()` survives for one job: bounding the upward `rmdir` sweep that
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
  `os.umask(0o007)` in `mkvlib.py` (every python `mkv-*` tool imports it), `umask 0007`
  in `mkv-frames` and `mkv-titlescan`, and
  `BASH_ENV=/etc/mediaops-profile.sh` for `kubectl exec -- bash -c`.
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
5. `shutil.move`, then byte-size equality source↔destination
6. `ffprobe` decode check on every moved file
7. cleanup of source directories only once empty

> Step 6 exists because it once silently did nothing: it was written with
> `ffprobe -f null -`, but `-f null` is an **ffmpeg** flag. `ffprobe` exited
> with "Unknown input format: null" on all 44 files, which read like 44
> warnings and actually meant the check never ran. **A checker that fails open
> is worse than no checker** — so `mkv-promote` treats a non-zero probe exit as
> fatal, and asserts a non-empty duration rather than merely a clean exit.

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
Play-Alls; it **cannot** confirm a real Play-All is being cut in the right
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

The pod must be **privileged** to see `/dev/sr*` and `/dev/sg*`, and pinned to
`styxeon`. It still runs as uid 3000; privilege exposes the device nodes and
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
message names the holder; staging is not the NAS; `MAKEMKV_KEY` is missing); 4 internal
error. `--selftest` is gone: the tests are `go test`.

**One controller at a time.** Whoever may spin a drive (a rip, a `finalize`, an
`identify`) holds an exclusive `flock` on `/tmp/mkv-rip.lease` for its whole life and
writes `{kind, run_id, pid, started_at}` into it, so a refusal can name it. Liveness is
the held lock, never a pid: the kernel releases the lock the instant its holder dies,
and a pod roll leaves an empty `/tmp`. A `running` or `finalizing` run whose lease is not
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
