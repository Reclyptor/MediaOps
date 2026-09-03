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

ENV PATH="/opt/mediaops/bin:${PATH}" \
    PYTHONPATH="/opt/mediaops/bin" \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    MEDIA_ROOT=/mnt/media \
    TZ=America/Chicago

USER media
WORKDIR /mnt/media

CMD ["sleep", "infinity"]
