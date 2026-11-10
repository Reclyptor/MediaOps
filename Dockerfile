# MakeMKV is closed-source and glibc-only; this image is musl. The bridge is the
# one jlesage/docker-makemkv uses: build and unpack on Debian, bundle the glibc
# runtime beside the binaries, and repoint each ELF's interpreter and rpath at the
# bundle so Alpine can run it without a glibc of its own. CLI only -- no Qt, no GUI.
FROM debian:12 AS makemkv

ARG MAKEMKV_VERSION=2.0.0
ARG MAKEMKV_OSS_SHA256=435316b2d219eb48c880526557addd076b5f5e6de5171424c7651f9cac95b161
ARG MAKEMKV_BIN_SHA256=f1265e74875a186efdfbbbec7459a64e969033515e53cbc4d805f0a374f0a124
# libmakemkv links libavcodec unconditionally, but only its audio-conversion path
# uses it and a rip never converts. Debian's libavcodec-dev would drag in ~100
# shared libraries (codecs, cairo, glib...) for nothing, so build the smallest
# static one instead: no programs, no autodetected external libraries.
ARG FFMPEG_VERSION=7.1.3
ARG FFMPEG_SHA256=f0bf043299db9e3caacb435a712fc541fbb07df613c4b893e8b77e67baf3adbe

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        build-essential pkg-config patchelf curl ca-certificates xz-utils nasm \
        libssl-dev libexpat1-dev zlib1g-dev libcurl4 \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /src
RUN fetch() { curl -fsSL -o "$1" "$2" && echo "$3  $1" | sha256sum -c - && mkdir "$4" && tar -xf "$1" --strip-components=1 -C "$4"; } \
 && fetch ffmpeg.tar.xz "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz" "${FFMPEG_SHA256}" ffmpeg \
 && fetch oss.tar.gz "https://www.makemkv.com/download/makemkv-oss-${MAKEMKV_VERSION}.tar.gz" "${MAKEMKV_OSS_SHA256}" oss \
 && fetch bin.tar.gz "https://www.makemkv.com/download/makemkv-bin-${MAKEMKV_VERSION}.tar.gz" "${MAKEMKV_BIN_SHA256}" bin

RUN cd ffmpeg \
 && ./configure --prefix=/usr/local --enable-static --disable-shared --enable-pic \
        --disable-autodetect --disable-programs --disable-doc \
 && make -j"$(nproc)" && make install

# Libraries only. mmccextr and mmgplsrv are the other things this tree builds and
# nothing here calls them.
RUN cd oss \
 && PKG_CONFIG_PATH=/usr/local/lib/pkgconfig ./configure --prefix=/ --disable-gui \
 && make -j"$(nproc)" \
 && make DESTDIR=/opt/makemkv install \
 && rm /opt/makemkv/bin/mmccextr /opt/makemkv/bin/mmgplsrv

# The bin tarball's `make install` stops to ask the user to type "yes" to its
# EULA, unless tmp/eula_accepted already exists. Pre-creating it is the same
# acceptance, made at build time by whoever builds this image.
RUN cd bin \
 && mkdir tmp && touch tmp/eula_accepted \
 && make DESTDIR=/opt/makemkv PREFIX=/ install

# makemkvcon dlopen()s libcurl, so ldd cannot see it; add it by hand and let the
# loop below pull in its dependencies. Everything ldd reports is copied in with
# its symlink chain, then the loader itself.
RUN cp -a /usr/lib/*/libcurl.so.4* /opt/makemkv/lib/ \
 && cd /opt/makemkv \
 && for f in bin/* lib/lib*.so*; do \
        [ -f "$f" ] && [ ! -L "$f" ] || continue; \
        LD_LIBRARY_PATH=/opt/makemkv/lib ldd "$f" | awk '/=> \//{print $3}'; \
    done | sort -u | while read -r dep; do \
        base=$(basename "$dep"); \
        [ -e "lib/$base" ] || cp -a "$(readlink -f "$dep")" "lib/$(basename "$(readlink -f "$dep")")"; \
        [ -e "lib/$base" ] || ln -s "$(basename "$(readlink -f "$dep")")" "lib/$base"; \
    done \
 && interp=$(patchelf --print-interpreter bin/makemkvcon) \
 && cp -L "$interp" "lib/$(basename "$interp")" \
 && for f in bin/*; do \
        [ -L "$f" ] && continue; \
        patchelf --set-interpreter "/opt/makemkv/lib/$(basename "$interp")" --set-rpath '$ORIGIN/../lib' "$f"; \
    done \
 && for f in lib/lib*.so*; do \
        [ -L "$f" ] || patchelf --set-rpath '$ORIGIN' "$f"; \
    done

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
#   sg3_utils    sg_start --eject, sg_raw/sg_turs: the tray and media probes that
#                never spin a disc (mkv-drives, mkv-rip)
#   lsscsi       cross-checking sr/sg pairing when sysfs is ambiguous
#   util-linux-misc  setsid, so a rip survives a dropped `kubectl exec`
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
        tzdata \
        sg3_utils \
        lsscsi \
        util-linux-misc

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

COPY --from=makemkv /opt/makemkv /opt/makemkv

# makemkvcon exits cleanly with no drives, so this is a real check, not a version
# print: it proves the patched interpreter and rpath resolve every bundled library
# on musl, and that the EULA was accepted. A bad bundle dies here at build time
# instead of in front of a disc. The MSG line is MakeMKV's own banner.
RUN /opt/makemkv/bin/makemkvcon -r info disc:9999 | tee /dev/stderr | grep -q '^MSG:1005,'

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

ENV PATH="/opt/mediaops/bin:/opt/makemkv/bin:${PATH}" \
    BASH_ENV="/etc/mediaops-profile.sh" \
    PYTHONPATH="/opt/mediaops/bin" \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    MEDIA_ROOT=/mnt/media \
    TZ=America/Chicago

USER media
WORKDIR /mnt/media

CMD ["sleep", "infinity"]
