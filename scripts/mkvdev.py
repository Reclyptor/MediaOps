"""Optical drives and MakeMKV plumbing shared by mkv-drives and mkv-rip.

Everything here is read-only against the hardware except eject(). Nothing
spins a disc up: tray and media state come from CDROM_DRIVE_STATUS on a
non-blocking fd, which asks the drive what it already knows rather than
reading a sector.
"""
import fcntl
import os
import re
import subprocess
import time

import mkvlib  # noqa: F401  -- sets the umask; settings.conf and rips must honour it

SYS_BLOCK = "/sys/class/block"

CDROM_DRIVE_STATUS = 0x5326
CDROMEJECT = 0x5309
CDS_NO_DISC, CDS_TRAY_OPEN, CDS_DRIVE_NOT_READY, CDS_DISC_OK = 1, 2, 3, 4

STATUS_NAMES = {CDS_NO_DISC: "no_disc", CDS_TRAY_OPEN: "tray_open",
                CDS_DRIVE_NOT_READY: "not_ready", CDS_DISC_OK: "disc_ok"}

MAKEMKV_DATA_DIR = "/work/makemkv"


def _usb_dir(sr):
    """The USB device directory behind /sys/class/block/<sr>, or None.

    The block device hangs off a SCSI host several levels below the USB device.
    The USB device is the nearest ancestor that has both `serial` and `speed`:
    interface directories (5-1.2.1:1.0) have neither, hubs further up have both
    but are not the drive. Walking up and taking the FIRST match keeps it the
    drive itself.
    """
    d = os.path.realpath(os.path.join(SYS_BLOCK, sr, "device"))
    while d not in ("/", "/sys"):
        if os.path.isfile(os.path.join(d, "serial")) and os.path.isfile(os.path.join(d, "speed")):
            return d
        d = os.path.dirname(d)
    return None


def _read(path):
    with open(path) as fh:
        return fh.read().strip()


def link_mbps(usb_dir):
    try:
        return int(float(_read(os.path.join(usb_dir, "speed"))))
    except (OSError, ValueError):
        return None


def drive_status(dev, sg):
    """-> (status, error). status is one of STATUS_NAMES' values or "unknown".

    The ioctl is the first choice: it needs only the sr node. When that node is
    missing or unreadable (an unprivileged pod sees sysfs but no /dev entries),
    fall back to TEST UNIT READY on the sg node, whose sense text says the same
    thing. If neither works the state is unknown, and that is reported rather
    than guessed -- a drive wrongly reported empty would be skipped silently.
    """
    try:
        fd = os.open(dev, os.O_RDONLY | os.O_NONBLOCK)
    except OSError as exc:
        ioctl_err = f"{dev}: {exc.strerror}"
    else:
        try:
            return STATUS_NAMES.get(fcntl.ioctl(fd, CDROM_DRIVE_STATUS, 0), "unknown"), None
        except OSError as exc:
            ioctl_err = f"{dev}: ioctl {exc.strerror}"
        finally:
            os.close(fd)

    if sg and os.path.exists(sg):
        r = subprocess.run(["sg_turs", sg], capture_output=True, text=True)
        if r.returncode == 0:
            return "disc_ok", None
        text = (r.stdout + r.stderr).lower()
        if "tray open" in text:
            return "tray_open", None
        if "medium not present" in text:
            return "no_disc", None
        if "becoming ready" in text or "not ready" in text:
            return "not_ready", None
    return "unknown", ioctl_err


def drives():
    """Every optical drive, in sr order.

    The contract fields (serial, dev, sg, usb_port, link_mbps, tray, media)
    plus `status` (the raw state, which distinguishes a drive still becoming
    ready from an empty one), `error` when state could not be read, and
    `usb_dir`, which mkv-rip's health watch needs and mkv-drives drops.
    """
    srs = sorted((n for n in os.listdir(SYS_BLOCK) if re.fullmatch(r"sr\d+", n)),
                 key=lambda n: int(n[2:]))
    out = []
    for sr in srs:
        usb = _usb_dir(sr)
        if usb is None:
            continue
        sg_dir = os.path.join(SYS_BLOCK, sr, "device", "scsi_generic")
        sgs = os.listdir(sg_dir) if os.path.isdir(sg_dir) else []
        dev = f"/dev/{sr}"
        sg = f"/dev/{sgs[0]}" if sgs else None
        status, err = drive_status(dev, sg)
        known = status != "unknown"
        rec = {
            "serial": _read(os.path.join(usb, "serial")),
            "dev": dev,
            "sg": sg,
            "usb_port": os.path.basename(usb),
            "link_mbps": link_mbps(usb),
            "tray": ("open" if status == "tray_open" else "closed") if known else None,
            "media": (status == "disc_ok") if known else None,
            "status": status,
            "usb_dir": usb,
        }
        if err:
            rec["error"] = err
        out.append(rec)
    return out


def eject(drive):
    """Open the tray, and prove it opened.

    makemkvcon leaves PREVENT MEDIUM REMOVAL set on the drive. With that lock on,
    CDROMEJECT still returns success while the BP60NB10 ignores it, so a return
    code proves nothing: the tray state is what counts. Release the lock, eject,
    then read the drive's status back. The sg path (ALLOW MEDIUM REMOVAL, then
    START STOP UNIT) is the fallback when the ioctl route leaves it closed.
    """
    tried = []
    if drive.get("sg"):
        r = subprocess.run(["sg_prevent", "--allow", drive["sg"]], capture_output=True, text=True)
        if r.returncode != 0:
            tried.append(f"sg_prevent: {(r.stderr or r.stdout).strip()[:120]}")
    try:
        fd = os.open(drive["dev"], os.O_RDONLY | os.O_NONBLOCK)
        try:
            fcntl.ioctl(fd, CDROMEJECT, 0)
        finally:
            os.close(fd)
    except OSError as exc:
        tried.append(f"{drive['dev']}: {exc.strerror}")
    if _tray_opened(drive):
        return
    tried.append("CDROMEJECT left the tray closed")
    if drive.get("sg"):
        r = subprocess.run(["sg_start", "--eject", drive["sg"]], capture_output=True, text=True)
        if r.returncode != 0:
            tried.append(f"sg_start: {(r.stderr or r.stdout).strip()[:120]}")
        if _tray_opened(drive):
            return
    raise OSError("; ".join(tried))


def _tray_opened(drive, timeout=5.0):
    """True once the drive reports its tray open (or empty), within `timeout`."""
    deadline = time.monotonic() + timeout
    while True:
        status, _ = drive_status(drive["dev"], drive.get("sg"))
        if status in ("tray_open", "no_disc"):
            return True
        if time.monotonic() >= deadline:
            return False
        time.sleep(0.5)


def split_fields(s):
    """Split a makemkvcon robot-mode field list: commas outside quotes, with
    backslash escapes inside them. Values are returned unquoted."""
    out, cur, quoted, i = [], [], False, 0
    while i < len(s):
        c = s[i]
        if quoted:
            if c == "\\" and i + 1 < len(s):
                cur.append(s[i + 1])
                i += 1
            elif c == '"':
                quoted = False
            else:
                cur.append(c)
        elif c == '"':
            quoted = True
        elif c == ",":
            out.append("".join(cur))
            cur = []
        else:
            cur.append(c)
        i += 1
    out.append("".join(cur))
    return out


def write_makemkv_settings():
    """Write ~/.MakeMKV/settings.conf from $MAKEMKV_KEY.

    The key is a licence, so it stays in the environment (a Secret) and on the
    pod's emptyDir -- never on the NAS, never in a log, never in an error
    message. Written to a sibling file and renamed so a concurrently starting
    makemkvcon never reads a half-written key. Mode 0600 is requested at
    creation, and the umask (0007) can only remove bits from that.
    """
    key = os.environ.get("MAKEMKV_KEY", "").strip()
    if not key:
        raise SystemExit("FATAL: MAKEMKV_KEY is not set; makemkvcon has no licence to run under.")
    if '"' in key or "\n" in key or "\\" in key:
        raise SystemExit("FATAL: MAKEMKV_KEY contains characters settings.conf cannot hold.")
    home = os.environ.get("HOME", "/work")
    conf_dir = os.path.join(home, ".MakeMKV")
    try:
        os.makedirs(conf_dir, mode=0o700, exist_ok=True)
        os.makedirs(MAKEMKV_DATA_DIR, exist_ok=True)
        tmp = os.path.join(conf_dir, f".settings.conf.{os.getpid()}")
        fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(fd, "w") as fh:
            fh.write(f'app_Key = "{key}"\napp_DataDir = "{MAKEMKV_DATA_DIR}"\n')
        os.replace(tmp, os.path.join(conf_dir, "settings.conf"))
    except OSError as exc:
        raise SystemExit(f"FATAL: cannot write MakeMKV settings under {home}: {exc.strerror}")


def disc_labels():
    """-> {"/dev/srN": label} from makemkvcon's drive list.

    `info disc:9999` is the cheap listing: it opens no title, but it does touch
    every drive that holds media, which is why mkv-drives makes it opt-in.
    """
    write_makemkv_settings()
    r = subprocess.run(["makemkvcon", "-r", "--cache=1", "info", "disc:9999"],
                       capture_output=True, text=True)
    by_sg = {d["sg"]: d["dev"] for d in drives() if d["sg"]}
    labels = {}
    for line in r.stdout.splitlines():
        if not line.startswith("DRV:"):
            continue
        f = split_fields(line[4:])
        if len(f) < 7 or f[1] == "256":
            continue
        dev = by_sg.get(f[6], f[6])
        labels[dev] = f[5] or None
    return labels
