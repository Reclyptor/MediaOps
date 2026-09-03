FROM alpine:3.22

LABEL org.opencontainers.image.title="mediaops" \
      org.opencontainers.image.description="Media-library processing workbench: mkvtoolnix, ffmpeg and the split/promote tooling, wired to the NAS" \
      org.opencontainers.image.source="https://github.com/Reclyptor/MediaOps"

# Every tool here is a stream-copy or a probe -- nothing re-encodes.
#   mkvtoolnix   mkvmerge (keyframe-exact chapter splitting), mkvpropedit (titles)
#   ffmpeg       ffprobe for duration/chapters/streams, ffmpeg for frame extraction
#   imagemagick  montage, for contact sheets
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
