# dHash parity fixture

`frames.gray` and `python.json` are what the Python tools produced, recorded once on
2026-11-14 from the scripts at `729d48e` (Python 3.14.10). The Go port must reproduce them
exactly; nothing regenerates them during tests.

`frames.gray` is 55 raw 9x8 gray frames (72 bytes each) followed by a 40-byte partial frame
the Python drops:

- 7 edge cases: all 0, all 255, increasing, decreasing, alternating, a bright left column,
  a dark right column;
- 40 frames of seeded random bytes (`random.Random(20261114)`);
- 8 real frames, one seek each, read over NFS as
  `ffmpeg -nostdin -v error -ss T -i FILE -map 0:v:0 -frames:v 1 -vf scale=9:8,format=gray -f rawvideo -`
  (ffmpeg 9.0.2) from `anime/Berserk/Season 01/Berserk - S01E01 - The Black Swordsman.mkv`
  and `anime/Cutie Honey/Season 01/Cutie Honey - S01E01 - The Black Claw Grips the Heart.mkv`
  at T = 95, 300, 610 and 1200 s, in that order.

`python.json` holds:

- `hashes`: `mkv-fingerprint`'s `_frames` over `frames.gray` (equal to `mkv-staticcheck`'s
  `hashes`, which the generator asserts), as 16 hex digits;
- `fingerprint_argv`, `staticcheck_argv`: the ffmpeg argv each script built;
- `tracks`: a planted series (t00..t05, each preview drawn from the next track's body with
  0-8 bits flipped, t02's preview made ambiguous with frames of t04, t05b an alternate cut of
  t05), and `scores`: the Python `score` of every previewed track against every other body;
- `match_stdout`, `dupcheck_stdout` (target `t05`), `staticcheck_stdout`: the scripts' own
  printed output. `mkv-dupcheck`'s control pair is whatever two files `os.listdir` returned
  first, so only its overlap figure is comparable.

Generator, run as `python3 -I gen.py SCRIPTS_DIR OUT_DIR` with `real.gray` beside it:

```python
"""Record the Python dHash and scoring outputs the Go port must reproduce.

Runs the real scripts/mkv-fingerprint, mkv-dupcheck and mkv-staticcheck from the
MediaOps checkout at master, with subprocess.run replaced so ffmpeg's stdout is
the recorded raw gray bytes. usage: python3 -I gen.py SCRIPTS_DIR OUT_DIR
"""
import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
import random
import subprocess
import sys
import tempfile
import types

scripts, out = sys.argv[1], sys.argv[2]
real_run = subprocess.run
here = os.path.dirname(os.path.abspath(__file__))


def load(name):
    loader = importlib.machinery.SourceFileLoader(name.replace("-", "_"), os.path.join(scripts, name))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    mod = importlib.util.module_from_spec(spec)
    loader.exec_module(mod)
    return mod


# frames.gray: synthetic edge cases, seeded noise, then the real frames, then a
# 40-byte partial frame the Python drops.
frames = []
frames.append(bytes(72))                                   # all equal -> 0
frames.append(bytes([255] * 72))
frames.append(bytes(range(72)))                            # increasing -> 0
frames.append(bytes(range(255, 255 - 72, -1)))             # decreasing -> all ones
frames.append(bytes([(i % 2) * 200 for i in range(72)]))   # alternating
frames.append(bytes([200 if (i % 9) == 0 else 10 for i in range(72)]))  # left column bright
frames.append(bytes([10 if (i % 9) == 8 else 200 for i in range(72)]))  # right column dark
rng = random.Random(20261114)
for _ in range(40):
    frames.append(bytes(rng.randrange(256) for _ in range(72)))
with open(os.path.join(here, "real.gray"), "rb") as fh:
    real = fh.read()
assert len(real) % 72 == 0
frames += [real[i:i + 72] for i in range(0, len(real), 72)]
raw = b"".join(frames) + bytes(rng.randrange(256) for _ in range(40))
with open(os.path.join(out, "frames.gray"), "wb") as fh:
    fh.write(raw)


class FakeRun:
    def __init__(self, stdout):
        self.stdout = stdout
        self.argv = []

    def __call__(self, cmd, **kw):
        self.argv.append(cmd)
        return types.SimpleNamespace(stdout=self.stdout, stderr=b"", returncode=0)


fp = load("mkv-fingerprint")
fake = FakeRun(raw)
fp.subprocess.run = fake
fp_hashes = fp._frames("/x.mkv", fp.BODY_FPS)
fp._frames("/x.mkv", fp.PREVIEW_FPS, ss=1234.5)
fp_argv = list(fake.argv)

sc = load("mkv-staticcheck")
fake2 = FakeRun(raw)
sc.subprocess.run = fake2
sc_hashes = sc.hashes("/x.mkv")
assert sc_hashes == fp_hashes

# Scoring: a synthetic series of fingerprints with planted adjacency. Each
# track's preview is drawn from the next track's body, with a few bits flipped
# (within and beyond the threshold) so the Hamming boundary is exercised.
def flip(h, n):
    for b in rng.sample(range(64), n):
        h ^= 1 << b
    return h


tracks = {}
names = [f"t{i:02d}" for i in range(6)]
bodies = {n: [rng.getrandbits(64) for _ in range(60)] for n in names}
for i, n in enumerate(names):
    rec = {"path": f"/m/{n}.mkv", "preview_start": None, "body": bodies[n], "preview": []}
    if i + 1 < len(names):
        nxt = bodies[names[i + 1]]
        pv = [flip(nxt[j], (j % 9)) for j in range(0, 40, 2)]   # 0..8 flips: <=6 hit, 7-8 miss
        pv += [rng.getrandbits(64) for _ in range(5)]
        rec["preview_start"] = 1300.0 + i
        rec["preview"] = pv
    tracks[n] = rec
# a weak one: t05's preview (none) is fine; make t02's preview ambiguous by
# borrowing half from t04's body too
tracks["t02"]["preview"] += [flip(bodies["t04"][j], 2) for j in range(0, 30, 2)]
# an alternate cut: t05b shares most of t05's body
tracks["t05b"] = {"path": "/m/t05b.mkv", "preview_start": None,
                  "body": [flip(h, 3) if k % 4 else rng.getrandbits(64) for k, h in enumerate(bodies["t05"])],
                  "preview": []}

with tempfile.TemporaryDirectory() as d:
    for n, rec in tracks.items():
        with open(os.path.join(d, n + ".json"), "w") as fh:
            json.dump(rec, fh)
    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        fp.cmd_match(d)
    match_out = buf.getvalue()

    dc_path = os.path.join(scripts, "mkv-dupcheck")
    dup_out = real_run([sys.executable, "-I", dc_path, d, "t05"], capture_output=True, text=True, check=True).stdout

scores = {}
for a in names:
    for b in tracks:
        if a != b and tracks[a]["preview"]:
            scores[f"{a}>{b}"] = fp.score(tracks[a]["preview"], tracks[b]["body"])

# staticcheck: verdicts on hash lists, through main() with hashes() replaced
static_cases = {
    "still.mkv": [0x1234] * 50 + [0x1235] * 3,
    "content.mkv": [rng.getrandbits(64) for _ in range(50)],
    "edge.mkv": [k % 3 for k in range(20)],   # ratio 0.15 exactly -> STATIC
    "empty.mkv": [],
}
sc.hashes = lambda p: static_cases[p]
sys_argv = sys.argv
sys.argv = ["mkv-staticcheck"] + list(static_cases)
buf = io.StringIO()
with contextlib.redirect_stdout(buf):
    sc.main()
static_out = buf.getvalue()
sys.argv = sys_argv

with open(os.path.join(out, "python.json"), "w") as fh:
    json.dump({
        "python": sys.version.split()[0],
        "frames": len(frames),
        "partial_bytes": 40,
        "hashes": [f"{h:016x}" for h in fp_hashes],
        "fingerprint_argv": fp_argv,
        "staticcheck_argv": fake2.argv,
        "tracks": {n: {"path": r["path"], "preview_start": r["preview_start"],
                       "body": [f"{h:016x}" for h in r["body"]],
                       "preview": [f"{h:016x}" for h in r["preview"]]} for n, r in tracks.items()},
        "scores": scores,
        "match_stdout": match_out,
        "dupcheck_target": "t05",
        "dupcheck_stdout": dup_out,
        "staticcheck_cases": {k: [f"{h:016x}" for h in v] for k, v in static_cases.items()},
        "staticcheck_stdout": static_out,
    }, fh, indent=1)
    fh.write("\n")
print("frames", len(frames), "hashes", len(fp_hashes))
```
