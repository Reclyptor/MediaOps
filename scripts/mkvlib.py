"""Episode-boundary detection for MakeMKV Play-All tracks.

Two earlier models failed and are worth recording:

1. Uniform k*D/N spacing -- broke on discs whose episodes differ in length.
2. Fixed "every M-th chapter" stride -- broke because chapters-per-episode is NOT
   constant. Fate Apocrypha Disc 01 runs 5,4,4,4,4 chapters per episode. That model
   also preferred 2-boundary solutions, since a single gap has zero spread.

Current model: walk the chapter list greedily, hopping ~E minutes at a time and
snapping to the nearest chapter mark. Handles variable chapters-per-episode, and
scores by fit error over a scan of candidate episode lengths E.
"""
import json
import os
import subprocess

MEDIA_ROOT = os.environ.get("MEDIA_ROOT", "/mnt/media")

# Where superseded files go, as one tree rather than a sibling of each share.
# Deliberately has NO default, unlike MEDIA_ROOT: guessing MEDIA_ROOT wrong finds
# nothing and fails loudly, while guessing this wrong writes real data onto the
# pod's overlay and reports success. Missing config must be a hard stop.
ARCHIVE_ROOT = os.environ.get("ARCHIVE_ROOT")

# Group-writability for `users` is the library convention. The NFS export supplies
# the GROUP (anongid=100); the MODE comes from the creating process, and the
# container default umask of 022 yields 644 files and 755 dirs -- no group write,
# wrong every time. `kubectl exec` does NOT run CMD or ENTRYPOINT, so an
# image-level wrapper cannot set this; the code that creates files must.
#
# This yields 660 files and 770 dirs. Note it does NOT reproduce the 770 seen on
# existing media files: umask can only clear permission bits, never add the
# execute bit, and nothing creates a file 0777 to begin with. That x bit is
# meaningless on a .mkv; what matters -- group `users` has rw -- is satisfied.
#
# Every mkv-* python tool imports mkvlib, so setting it here covers all of them
# at once. Import-time side effects are normally worth avoiding, but the
# alternative is repeating it in a dozen entry points and silently omitting it
# from the next one.
os.umask(0o007)


def archive_root():
    """The configured archive root, checked to be real storage before it is used.

    The archive is reached by configuration now, not by construction from the
    file's own path, and configuration can be absent or wrong. Both failures land
    on the pod's overlay, which accepts the write, reports success, and discards
    it at the next image roll. `/mnt/media` and `/` report the same st_dev, so
    comparing against `/` catches the overlay exactly.

    It never creates the root. A mkdir would itself succeed on the overlay and
    produce precisely the state this is meant to detect.
    """
    if not ARCHIVE_ROOT:
        raise SystemExit(
            "FATAL: ARCHIVE_ROOT is not set. It has no default on purpose -- a "
            "guessed archive path lands on the pod's overlay and is lost at the "
            "next redeploy. Set it in apps/mediaops/deployment.yaml.")
    root = os.path.abspath(ARCHIVE_ROOT)
    if not os.path.isdir(root):
        raise SystemExit(
            f"FATAL: ARCHIVE_ROOT does not exist: {root}\n"
            "Create it on the NAS. Creating it from the pod would succeed on the "
            "overlay and look correct.")
    if os.stat(root).st_dev == os.stat("/").st_dev:
        raise SystemExit(
            f"FATAL: ARCHIVE_ROOT is on the pod's root filesystem: {root}\n"
            "That is the ephemeral overlay -- the mount is missing. Anything "
            "written there is discarded at the next redeploy.")
    # An unexported child dataset is the other way this goes wrong, and it does
    # not announce itself: the NAS serves the parent's empty mountpoint stub, so
    # the path exists, sits on the right device, and is simply the wrong
    # directory -- root-owned, and the one thing it is not is writable. Testing
    # for a mount point would not catch it; the child exports that DO work are
    # not mount points on the client either.
    if not os.access(root, os.W_OK | os.X_OK):
        raise SystemExit(
            f"FATAL: ARCHIVE_ROOT is not writable: {root}\n"
            "If it is a child dataset on the NAS, check it is listed in "
            "/etc/exports -- an unexported child shows through the parent "
            "export as an empty, root-owned directory.")
    return root


def share_root(path):
    """The share `path` actually lives on: its nearest enclosing mount point.

    MEDIA_ROOT began as a single NFS mount, so a synthesized sibling such as
    MEDIA_ROOT/_archive necessarily landed on that share. It is now a plain
    container directory with one mount per NAS beneath it (dxp6800/, dxp4800/),
    and that same sibling resolves to the pod's own overlay filesystem --
    writable, silent, and discarded on the next redeploy. A `shutil.move` there
    reports success and destroys the file at the next image roll.

    Resolving the share from the file itself keeps working whether MEDIA_ROOT is
    one mount or twenty, and needs no list of share names.

    The archive no longer uses this -- it is one configured tree, see
    archive_root(). What remains is bounding the upward rmdir sweep that clears
    directories an archive move emptied, so it can never climb out of the share.
    """
    p = os.path.abspath(path)
    if not os.path.isdir(p):
        p = os.path.dirname(p)
    while not os.path.ismount(p):
        parent = os.path.dirname(p)
        if parent == p:
            return p
        p = parent
    return p

# Plex-illegal characters. ':' and '?' have accepted substitutions; the rest are
# simply dropped. Matches the anime-normalize skill.
_ILLEGAL = str.maketrans({c: None for c in '/*"<>|\\'})


def clean(name):
    """Make a title safe for a filename without altering the words."""
    return name.replace(":", " - ").replace("?", "？").translate(_ILLEGAL)


def decode_check(path):
    """-> (path, ok, detail). A readable positive duration is the evidence.

    This was once written as `ffprobe -v error -i FILE -f null -`, but `-f null`
    is an *ffmpeg* flag. ffprobe rejected it with "Unknown input format: null" on
    every file, which read like a warning and actually meant the check never ran.
    A checker that fails open is worse than no checker -- so this asserts a
    positive duration rather than merely a zero exit status.
    """
    r = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration",
         "-of", "default=nw=1:nk=1", path],
        capture_output=True, text=True)
    if r.returncode != 0:
        return path, False, (r.stderr.strip() or "ffprobe failed")[:200]
    try:
        d = float(r.stdout.strip())
    except ValueError:
        return path, False, f"unreadable duration: {r.stdout.strip()[:80]!r}"
    if d <= 0:
        return path, False, f"non-positive duration {d}"
    return path, True, f"{d:.2f}s"


def chapters(path):
    o = subprocess.run(["ffprobe", "-v", "error", "-show_chapters", "-of", "json", path],
                       capture_output=True, text=True).stdout
    try:
        c = json.loads(o).get("chapters", [])
    except Exception:
        c = []
    return [float(x["start_time"]) / 60 for x in c]


def duration(path):
    return float(subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format=duration",
                                 "-of", "default=nw=1:nk=1", path],
                                capture_output=True, text=True).stdout) / 60


def _walk(D, st, E, tol):
    """Greedy: from each boundary, jump ~E and snap to the nearest chapter."""
    if not st:
        return None
    b = [st[0]]
    if b[0] > 3.0:
        return None                      # a long lead-in means we are not at episode 1
    err = 0.0
    while True:
        target = b[-1] + E
        if target > D - 0.55 * E:
            break                        # remaining tail is the final episode
        best = min(st, key=lambda c: abs(c - target))
        if abs(best - target) > tol:
            return None
        if best <= b[-1] + 1.0:
            return None
        err += abs(best - target)
        b.append(best)
    if len(b) < 2:
        return None
    gaps = [b[i + 1] - b[i] for i in range(len(b) - 1)]
    tail = D - b[-1]
    if not all(E - tol <= g <= E + tol for g in gaps):
        return None
    # the last segment may carry end credits or a bonus, so it gets a looser bound
    if not (0.55 * E <= tail <= 1.9 * E):
        return None
    return b, err / max(len(b) - 1, 1), gaps, tail


def detect(D, st, lo=9.0, hi=36.0, tol=1.5, hint=None):
    """-> (boundaries, ep_len, mean_err, tail) or None.

    `hint` is the series' real per-episode runtime. Without it the scan happily
    locks onto HALF an episode: anime discs put a chapter at the mid-episode
    eyecatch, so E=12 fits a 24-minute show just as tightly as E=24. Sailor Moon
    Disc 01 came back as 16 episodes instead of 8 that way.
    """
    if hint:
        lo, hi = hint * 0.82, hint * 1.22
    best = None
    E = lo
    while E <= hi:
        r = _walk(D, st, E, tol)
        if r:
            b, err, gaps, tail = r
            avg = sum(gaps) / len(gaps)
            cand = (-len(b), err, b, avg, tail)
            if best is None or cand < best:
                best = cand
        E += 0.05
    if not best:
        return None
    _n, err, b, avg, tail = best
    return b, avg, err, tail


# Observed fit_err across 121 real tracks: genuine Play-Alls land at 0.002-0.31,
# while bonus features that merely LOOK periodic land at 0.48-1.13. A bonus track
# will still produce a confident-looking split -- the Sailor Moon SuperS Special
# (one 3-segment featurette) came back as "2 episodes of 29.88m" and would have
# been cut into a 29.88m file plus a 17.68m orphan. Two independent tells catch it:
# the fit error, and the detected episode length drifting from the published one.
#
# LIMIT OF THIS MODULE -- read before trusting any output.
# A set of cuts shifted by a CONSTANT is exactly as regular as the correct set:
# same stride, same spread, same fit error, same episode length. No statistic here
# can tell them apart. Confirmed on Demon Slayer S02/Disc 02, which grades "auto"
# with fit_err 0.226 and yet cuts 1.57 min INSIDE each episode.
# Therefore: grade() screens out tracks that are not Play-Alls. It CANNOT verify
# that a real Play-All is being cut in the right place. Only frames can.
AUTO_MAX_ERR = 0.35
AUTO_MAX_LEN_DRIFT = 0.12


def grade(ep_len, err, hint):
    """-> 'auto' | 'review'. 'review' still yields a split line, but it must be
    content-verified before it is run."""
    if err > AUTO_MAX_ERR:
        return "review"
    if hint and abs(ep_len - hint) / hint > AUTO_MAX_LEN_DRIFT:
        return "review"
    return "auto"


def split_chapter_indices(st, b):
    """mkvmerge --split chapters: wants 1-based chapter numbers at which a NEW file
    starts. Splitting on chapter numbers is keyframe-exact; timecodes are not."""
    return [st.index(x) + 1 for x in b[1:]]
