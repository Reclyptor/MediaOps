"""What is left of the Python tools' shared library.

The episode-boundary detector and the probe helpers moved to Go (internal/bounds,
kit/ffprobe) with mkv-probe, mkv-bounds, mkv-split and mkv-order. The two
functions below stay only because anime-normalize's mkv-split-ts.py still
imports them; this file goes with the last Python in the image.
"""
import os
import subprocess

# Group-writability for `users` is the library convention. The MODE comes from
# the creating process, and the container default umask of 022 yields 644 files
# and 755 dirs -- no group write, wrong every time. `kubectl exec` does NOT run
# CMD or ENTRYPOINT, so an image-level wrapper cannot set this; the code that
# creates files must. This yields 660 files and 770 dirs.
os.umask(0o007)


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


def duration(path):
    return float(subprocess.run(["ffprobe", "-v", "error", "-show_entries", "format=duration",
                                 "-of", "default=nw=1:nk=1", path],
                                capture_output=True, text=True).stdout) / 60
