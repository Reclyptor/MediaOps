FROM alpine:3.22

LABEL org.opencontainers.image.title="mediaops" \
      org.opencontainers.image.description="Media-library processing workbench: mkvtoolnix, ffmpeg and the split/promote tooling, wired to the NAS" \
      org.opencontainers.image.source="https://github.com/Reclyptor/MediaOps"

# Every tool here is a stream-copy or a probe -- nothing re-encodes.
#   mkvtoolnix   mkvmerge (keyframe-exact chapter splitting), mkvpropedit (titles)
#   ffmpeg       ffprobe for duration/chapters/streams, ffmpeg for frame extraction
#   imagemagick  montage, for ad-hoc tiling (the mkv-* tools tile with ffmpeg)
#   font-dejavu  NOT optional: without a font, drawtext/annotate silently produce
#                unlabelled tiles, and an unlabelled contact sheet cannot be used
#                to verify a cut point
#   coreutils &c GNU behaviour; busybox applets differ enough to break scripts subtly
RUN apk add --no-cache \
        mkvtoolnix \
        ffmpeg \
        imagemagick \
        font-dejavu \
        mediainfo \
        python3 \
        ca-certificates \
        bash \
        coreutils \
        findutils \
        grep \
        sed \
        gawk \
        jq \
        rsync \
        tzdata

# Alpine's ImageMagick 7 ships no type.xml, so it has no font to fall back on and
# ANY unqualified -label/-annotate dies with "unable to read font `'" -- even
# though font-dejavu is installed and fontconfig can see it. Setting MAGICK_FONT
# does not help; only a typemap does. Without this, montage is a tool that looks
# present and is not.
RUN printf '%s\n' \
    '<?xml version="1.0"?>' \
    '<typemap>' \
    '  <type name="helvetica" fullname="Helvetica" family="Helvetica" glyphs="/usr/share/fonts/dejavu/DejaVuSans.ttf"/>' \
    '  <type name="DejaVu-Sans" fullname="DejaVu Sans" family="DejaVu Sans" glyphs="/usr/share/fonts/dejavu/DejaVuSans.ttf"/>' \
    '  <type name="DejaVu-Sans-Bold" fullname="DejaVu Sans Bold" family="DejaVu Sans" style="normal" weight="700" glyphs="/usr/share/fonts/dejavu/DejaVuSans-Bold.ttf"/>' \
    '  <type name="DejaVu-Sans-Mono" fullname="DejaVu Sans Mono" family="DejaVu Sans Mono" glyphs="/usr/share/fonts/dejavu/DejaVuSansMono.ttf"/>' \
    '</typemap>' > /etc/ImageMagick-7/type.xml \
 && convert -size 32x32 xc:red /tmp/_t.png \
 && montage -label probe /tmp/_t.png /tmp/_m.png \
 && test -s /tmp/_m.png \
 && rm -f /tmp/_t.png /tmp/_m.png

# uid 3000 / gid 100 matches every file under /mnt/primary/videos (mode 770, group
# `users`). Running as this user is what makes a root-owned media file impossible
# rather than merely discouraged. gid 100 is Alpine's existing `users` group.
RUN adduser -u 3000 -G users -D -H -s /bin/bash media

COPY --chown=3000:100 scripts/ /opt/mediaops/bin/
RUN chmod 0755 /opt/mediaops/bin/*

# The library convention is group-writable by `users`. The NFS export supplies the
# GROUP (anongid=100); the MODE comes from the creating process, and the container
# default umask of 022 yields 644 files and 755 dirs -- no group write, wrong every
# time. umask 0007 gives 660 files and 770 dirs; it cannot give 770 files, because
# umask only clears bits and never adds the execute bit.
#
# `kubectl exec` runs its command DIRECTLY: it does not go through CMD or
# ENTRYPOINT, so neither can set a umask for exec'd work. Coverage is therefore
# per-caller, in three places:
#   - the python mkv-* tools  -> os.umask(0o007) in mkvlib.py, which they all import
#   - mkv-frames, mkv-titlescan -> `umask 0007` in the script
#   - `kubectl exec -- bash -c` -> BASH_ENV below, which bash sources even
#                                  non-interactively
# NOT covered: `kubectl exec -- sh -c '...'`. Alpine's /bin/sh is busybox ash,
# which reads $ENV only when interactive, so an ad-hoc `sh -c 'mkdir ...'` still
# inherits umask 022. Use `bash -c`, or chmod explicitly.
RUN printf 'umask 0007\n' > /etc/mediaops-profile.sh \
 && chmod 0644 /etc/mediaops-profile.sh

ENV PATH="/opt/mediaops/bin:${PATH}" \
    BASH_ENV="/etc/mediaops-profile.sh" \
    PYTHONPATH="/opt/mediaops/bin" \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    MEDIA_ROOT=/mnt/media \
    TZ=America/Chicago

USER media
WORKDIR /mnt/media

CMD ["sleep", "infinity"]
